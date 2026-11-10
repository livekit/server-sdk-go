package media

import (
	"errors"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/gammazero/deque"
	"github.com/google/uuid"
	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/opus"
	protoLogger "github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/hwstats"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

type PCMLocalTrackParams struct {
	Encryptor     Encryptor
	EnableStats   bool
	EnableHWStats bool
}

type PCMLocalTrackOption func(*PCMLocalTrackParams)

func WithEncryptor(encryptor Encryptor) PCMLocalTrackOption {
	return func(p *PCMLocalTrackParams) {
		p.Encryptor = encryptor
	}
}

func WithTrackStats() PCMLocalTrackOption {
	return func(p *PCMLocalTrackParams) {
		p.EnableStats = true
	}
}

func WithHWStats() PCMLocalTrackOption {
	return func(p *PCMLocalTrackParams) {
		p.EnableHWStats = true
	}
}

type PCMLocalTrack struct {
	*webrtc.TrackLocalStaticSample

	opusWriter         media.WriteCloser[opus.Sample]
	pcmWriter          media.WriteCloser[media.PCM16Sample]
	resampledPCMWriter media.WriteCloser[media.PCM16Sample]

	sourceSampleRate int
	frameDuration    time.Duration
	sourceChannels   int
	framesRead       int64
	encodeRate       int

	// Playout ends once the encoder has seen the stream up to the last real sample, plus a frame for its
	// look-ahead. Both are stream positions in frames, counting the silence sent between writes.
	realEnd       atomic.Int64
	encodedFrames *atomic.Int64

	// int16 to support a LE/BE PCM16 chunk that has a high byte and low byte
	// TODO(anunaym14): switch out deque for a ring buffer
	chunkBuffer *deque.Deque[media.PCM16Sample]
	numSamples  atomic.Int64

	mu   sync.Mutex
	cond *sync.Cond

	emptyBufMu   sync.Mutex
	emptyBufCond *sync.Cond

	started core.Fuse
	closed  core.Fuse

	muted atomic.Bool
	bound atomic.Bool

	logger         protoLogger.Logger
	enableStats    bool
	loggingEnabled bool
	logState       pcmLocalTrackLogState
	cpuStats       *hwstats.CPUStats
	memStats       *hwstats.MemoryStats
}

type pcmLocalTrackLogState struct {
	at             time.Time
	totalWritten   uint64
	totalProcessed uint64
	prevWritten    uint64
	prevProcessed  uint64
}

// NewPCMLocalTrack creates a wrapper around a webrtc.TrackLocalStaticSample that accepts PCM16 samples via the WriteSample method,
// encodes them to opus, and writes them to the track.
// PCMLocalTrack can directly be used as a local track to publish to a room.
// The sourceSampleRate and sourceChannels are the sample rate and channels of the source audio.
func NewPCMLocalTrack(
	sourceSampleRate int,
	sourceChannels int,
	logger protoLogger.Logger,
	opts ...PCMLocalTrackOption,
) (*PCMLocalTrack, error) {
	if sourceChannels <= 0 || sourceChannels > 2 || sourceSampleRate <= 0 {
		return nil, errors.New("invalid source sample rate or channels")
	}

	params := &PCMLocalTrackParams{
		Encryptor: nil,
	}
	for _, opt := range opts {
		opt(params)
	}

	id := uuid.New().String()[:5]
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "go_track"+id, "go_stream"+id)
	if err != nil {
		return nil, err
	}

	// opusWriter writes opus payloads to the track
	var opusWriter media.WriteCloser[opus.Sample]
	if params.Encryptor != nil {
		encryptionHandler := newEncryptionHandler(track, params.Encryptor, sourceSampleRate)
		opusWriter = media.FromSampleWriter[opus.Sample](encryptionHandler, sourceSampleRate, defaultPCMFrameDuration)
	} else {
		opusWriter = media.FromSampleWriter[opus.Sample](track, DefaultOpusSampleRate, defaultPCMFrameDuration)
	}
	encodedFrames := atomic.NewInt64(0)
	pcmWriter, resampledPCMWriter, err := newLocalEncoder(opusWriter, sourceSampleRate, sourceChannels, encodedFrames, logger)
	if err != nil {
		return nil, err
	}

	var cpuStats *hwstats.CPUStats
	var memStats *hwstats.MemoryStats
	if params.EnableHWStats {
		cpuStats, _ = hwstats.NewCPUStats(nil)
		memStats, _ = hwstats.NewMemoryStats()
	}

	// the final chain of writers:
	// WriteSample -> resamplesPCMWriter (resamples source to target sample rate as necessary)
	// -> PCMWriter (encodes PCM -> Opus)
	// -> opusWriter (writes opus frames to the track) -> track
	t := &PCMLocalTrack{
		TrackLocalStaticSample: track,
		opusWriter:             opusWriter,
		pcmWriter:              pcmWriter,
		resampledPCMWriter:     resampledPCMWriter,
		sourceSampleRate:       sourceSampleRate,
		frameDuration:          defaultPCMFrameDuration,
		sourceChannels:         sourceChannels,
		encodeRate:             pcmWriter.SampleRate(),
		encodedFrames:          encodedFrames,
		chunkBuffer:            new(deque.Deque[media.PCM16Sample]),
		logger:                 logger,
		enableStats:            params.EnableStats,
		loggingEnabled:         params.EnableStats || params.EnableHWStats,
		cpuStats:               cpuStats,
		memStats:               memStats,
		logState: pcmLocalTrackLogState{
			at: time.Now(),
		},
	}

	t.cond = sync.NewCond(&t.mu)
	t.emptyBufCond = sync.NewCond(&t.emptyBufMu)
	return t, nil
}

func (t *PCMLocalTrack) Bind(trackLocal webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	parameters, err := t.TrackLocalStaticSample.Bind(trackLocal)
	if err == nil {
		t.bound.Store(true)
		t.started.Once(func() { go t.processSamples() })
	}
	return parameters, err
}

// Encodes PCM to opus at the opus writer's rate, resampling from the source rate first when they differ.
func newLocalEncoder(w media.WriteCloser[opus.Sample], sourceRate, channels int, encodedFrames *atomic.Int64, logger protoLogger.Logger) (enc, resampled media.WriteCloser[media.PCM16Sample], err error) {
	enc, err = opus.Encode(w, channels, logger)
	if err != nil {
		return nil, nil, err
	}
	var counted media.WriteCloser[media.PCM16Sample] = &frameCounter{WriteCloser: enc, channels: channels, frames: encodedFrames}
	resampled = counted
	if enc.SampleRate() != sourceRate {
		// A resampled frame is not always a whole opus frame, and opus takes only whole frames, so a shorter tail is dropped.
		framed := media.FullFrames(counted, frameSamples(0, enc.SampleRate(), channels, defaultPCMFrameDuration), media.DropTailOnClose[media.PCM16Sample]())
		resampled = media.ResampleWriter(framed, sourceRate, media.WithResampleChannels(channels))
	}
	return enc, resampled, nil
}

type frameCounter struct {
	media.WriteCloser[media.PCM16Sample]
	channels int
	frames   *atomic.Int64
}

func (c *frameCounter) WriteSample(s media.PCM16Sample) error {
	defer c.frames.Add(int64(len(s) / c.channels))
	return c.WriteCloser.WriteSample(s)
}

func (t *PCMLocalTrack) playedOut() bool {
	end := t.realEnd.Load()
	if end == 0 {
		return true
	}
	lookAhead := int64(frameSamples(0, t.encodeRate, 1, t.frameDuration))
	return t.encodedFrames.Load()*int64(t.sourceSampleRate) >= end*int64(t.encodeRate)+lookAhead*int64(t.sourceSampleRate)
}

// Sizes alternate when a frame is not a whole number of samples, so frames add up to exactly the source rate.
func frameSamples(n int64, rate, channels int, frameDur time.Duration) int {
	perSec := int64(time.Second / frameDur)
	return int((n+1)*int64(rate)/perSec-n*int64(rate)/perSec) * channels
}

func (t *PCMLocalTrack) getFrameFromChunkBuffer() media.PCM16Sample {
	if t.closed.IsBroken() && t.numSamples.Load() == 0 && t.playedOut() {
		return nil
	}

	start := t.framesRead * int64(t.sourceSampleRate) / int64(time.Second/t.frameDuration)
	samples := frameSamples(t.framesRead, t.sourceSampleRate, t.sourceChannels, t.frameDuration)
	t.framesRead++
	frame := make(media.PCM16Sample, 0, samples)
	for len(frame) < samples && t.chunkBuffer.Len() != 0 {
		chunk := t.chunkBuffer.PopFront()
		remaining := min(samples-len(frame), len(chunk))
		frame = append(frame, chunk[:remaining]...)
		if remaining < len(chunk) {
			t.chunkBuffer.PushFront(chunk[remaining:])
		}
		// Set before the queue count drops, so a waiter that sees an empty queue also sees where the audio ends.
		t.realEnd.Store(start + int64(len(frame)/t.sourceChannels))
		t.numSamples.Sub(int64(remaining))
	}

	if len(frame) < samples {
		frame = append(frame, make(media.PCM16Sample, samples-len(frame))...)
	}
	return frame
}

func (t *PCMLocalTrack) WriteSample(chunk media.PCM16Sample) error {
	if t.closed.IsBroken() {
		return errors.New("track is closed")
	}

	if t.muted.Load() || len(chunk) == 0 || !t.bound.Load() {
		return nil
	}

	chunkCopy := make(media.PCM16Sample, len(chunk))
	copy(chunkCopy, chunk)

	t.mu.Lock()
	t.chunkBuffer.PushBack(chunkCopy)
	t.numSamples.Add(int64(len(chunkCopy)))
	t.cond.Broadcast()
	var snapshot *pcmLocalTrackLogSnapshot
	if t.loggingEnabled {
		t.logState.totalWritten += uint64(len(chunk))
		snapshot = t.collectLogSnapshotLocked(time.Now())
	}
	t.mu.Unlock()
	if snapshot != nil {
		t.emitLogSnapshot(snapshot)
	}
	return nil
}

func (t *PCMLocalTrack) processSamples() {
	ticker := time.NewTicker(t.frameDuration)
	defer ticker.Stop()

	for !t.closed.IsBroken() || t.numSamples.Load() != 0 || !t.playedOut() {
		var frame media.PCM16Sample
		var snapshot *pcmLocalTrackLogSnapshot

		t.mu.Lock()
		frame = t.getFrameFromChunkBuffer()
		if frame != nil && t.loggingEnabled {
			t.logState.totalProcessed += uint64(len(frame))
			snapshot = t.collectLogSnapshotLocked(time.Now())
		}
		t.mu.Unlock()

		if frame != nil {
			t.resampledPCMWriter.WriteSample(frame)
			if snapshot != nil {
				t.emitLogSnapshot(snapshot)
			}
		}
		if t.numSamples.Load() == 0 {
			t.emptyBufMu.Lock()
			t.emptyBufCond.Broadcast()
			t.emptyBufMu.Unlock()
		}

		<-ticker.C
	}

	// closing the writers here because we continue to write on close
	// until the buffer is empty
	t.resampledPCMWriter.Close()
	t.pcmWriter.Close()
	t.opusWriter.Close()
}

func (t *PCMLocalTrack) setMuted(muted bool) error {
	if t.closed.IsBroken() {
		return errors.New("track is closed")
	}

	// Pending samples are dropped but mute but,
	// we continue to write silence on mute to not
	// mess up the RTP timestamps.
	if !t.muted.Swap(muted) && muted {
		t.ClearQueue()
	}
	return nil
}

func (t *PCMLocalTrack) GetMuteFunc(muted bool) lksdk.Private[lksdk.MuteFunc] {
	return lksdk.MakePrivate(lksdk.MuteFunc(t.setMuted))
}

func (t *PCMLocalTrack) WaitForPlayout() {
	t.emptyBufMu.Lock()
	defer t.emptyBufMu.Unlock()

	for t.numSamples.Load() > 0 || !t.playedOut() {
		t.emptyBufCond.Wait()
	}
}

func (t *PCMLocalTrack) ClearQueue() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.chunkBuffer.Clear()
	t.numSamples.Store(0)

	t.emptyBufMu.Lock()
	t.emptyBufCond.Broadcast()
	t.emptyBufMu.Unlock()
}

func (t *PCMLocalTrack) Close() error {
	t.closed.Once(func() {
		t.mu.Lock()
		t.cond.Broadcast()
		t.mu.Unlock()
		if t.cpuStats != nil {
			t.cpuStats.Stop()
		}
	})
	return nil
}

func (t *PCMLocalTrack) SampleRate() int {
	return t.sourceSampleRate
}

func (t *PCMLocalTrack) String() string {
	return "PCMLocalTrack"
}

type pcmLocalTrackLogSnapshot struct {
	interval       time.Duration
	queueSamples   int64
	totalWritten   uint64
	totalProcessed uint64
	deltaWritten   uint64
	deltaProcessed uint64
}

func (t *PCMLocalTrack) collectLogSnapshotLocked(now time.Time) *pcmLocalTrackLogSnapshot {
	if t.logState.at.IsZero() {
		t.logState.at = now
		return nil
	}

	const logInterval = 5 * time.Second
	if now.Sub(t.logState.at) < logInterval {
		return nil
	}

	interval := now.Sub(t.logState.at)
	snapshot := &pcmLocalTrackLogSnapshot{
		interval:       interval,
		queueSamples:   t.numSamples.Load(),
		totalWritten:   t.logState.totalWritten,
		totalProcessed: t.logState.totalProcessed,
		deltaWritten:   t.logState.totalWritten - t.logState.prevWritten,
		deltaProcessed: t.logState.totalProcessed - t.logState.prevProcessed,
	}

	t.logState.at = now
	t.logState.prevWritten = t.logState.totalWritten
	t.logState.prevProcessed = t.logState.totalProcessed

	return snapshot
}

func (t *PCMLocalTrack) emitLogSnapshot(snapshot *pcmLocalTrackLogSnapshot) {
	if snapshot == nil || snapshot.interval <= 0 {
		return
	}

	elapsed := snapshot.interval.Seconds()
	if elapsed == 0 {
		return
	}

	fields := make([]interface{}, 0, 20)

	if t.enableStats {
		chanCount := float64(t.sourceChannels)
		if chanCount == 0 {
			chanCount = 1
		}

		ingressHz := float64(snapshot.deltaWritten) / chanCount / elapsed

		processedSamples := float64(snapshot.deltaProcessed)
		if t.sourceSampleRate != 0 && t.sourceSampleRate != DefaultOpusSampleRate {
			processedSamples *= float64(DefaultOpusSampleRate) / float64(t.sourceSampleRate)
		}
		egressHz := processedSamples / chanCount / elapsed

		queueSeconds := 0.0
		if t.sourceSampleRate != 0 {
			queueSeconds = float64(snapshot.queueSamples) / (chanCount * float64(t.sourceSampleRate))
		}

		fields = append(fields,
			"interval_s", elapsed,
			"ingress_hz", ingressHz,
			"egress_hz", egressHz,
			"queue_samples", snapshot.queueSamples,
			"queue_s", queueSeconds,
			"total_written", snapshot.totalWritten,
			"total_processed", snapshot.totalProcessed,
			"source_sample_rate", t.sourceSampleRate,
		)
	}

	if t.cpuStats != nil {
		fields = append(fields,
			"cpu_load", t.cpuStats.GetCPULoad(),
			"num_cpu", t.cpuStats.NumCPU(),
		)
	}

	if t.memStats != nil {
		used, total, err := t.memStats.GetMemory()
		if err == nil {
			fields = append(fields, "mem_used", used, "mem_total", total)
		}
	}

	t.logger.Infow("pcm local track stats", fields...)
}
