package media

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/opus"
	"github.com/livekit/media-sdk/rtp"
	protoLogger "github.com/livekit/protocol/logger"
	buffer "github.com/livekit/server-sdk-go/v2/pkg/lockless_circular_buffer"
)

const (
	DefaultOpusSampleRate     = 48000
	DefaultOpusSampleDuration = 20 * time.Millisecond
	defaultPCMSampleDuration  = 10000 * time.Microsecond
	// todo(anunaym14): make this configurable
	defaultBufferCapacity = 32768
)

type PCMLocalTrackParams struct {
	WriteSilenceOnNoData bool
}

type PCMLocalTrackOption func(*PCMLocalTrackParams)

func WithWriteSilenceOnNoData(writeSilenceOnNoData bool) PCMLocalTrackOption {
	return func(p *PCMLocalTrackParams) {
		p.WriteSilenceOnNoData = writeSilenceOnNoData
	}
}

type PCMLocalTrack struct {
	*webrtc.TrackLocalStaticSample

	opusWriter         media.WriteCloser[opus.Sample]
	pcmWriter          media.WriteCloser[media.PCM16Sample]
	resampledPCMWriter media.WriteCloser[media.PCM16Sample]

	sourceSampleRate     int
	frameDuration        time.Duration
	sourceChannels       int
	chunksPerSample      int
	writeSilenceOnNoData bool

	chunkBuffer *buffer.CircularBuffer[int16]

	writeBufMu   sync.Mutex
	writeBufCond *sync.Cond
	emptyBufMu   sync.Mutex
	emptyBufCond *sync.Cond

	closed atomic.Bool
}

// NewPCMLocalTrack creates a wrapper around a webrtc.TrackLocalStaticSample that accepts PCM16 samples via the WriteSample method,
// encodes them to opus, and writes them to the track.
// PCMLocalTrack can directly be used as a local track to publish to a room.
// The sourceSampleRate and sourceChannels are the sample rate and channels of the source audio.
// It also provides an option to write silence when no data is available, which is disabled by default.
// Stereo tracks are not supported, they may result in unpleasant audio.
func NewPCMLocalTrack(sourceSampleRate int, sourceChannels int, logger protoLogger.Logger, opts ...PCMLocalTrackOption) (*PCMLocalTrack, error) {
	if sourceChannels <= 0 || sourceChannels > 2 || sourceSampleRate <= 0 {
		return nil, errors.New("invalid source sample rate or channels")
	}

	params := &PCMLocalTrackParams{
		WriteSilenceOnNoData: false,
	}
	for _, opt := range opts {
		opt(params)
	}

	id := uuid.New().String()[:5]
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "go_track"+id, "go_stream"+id)
	if err != nil {
		return nil, err
	}

	// opusWriter writes opus samples to the track
	opusWriter := media.FromSampleWriter[opus.Sample](track, DefaultOpusSampleRate, defaultPCMSampleDuration)
	// pcmWriter encodes opus samples from PCM16 samples and writes them to opusWriter
	pcmWriter, err := opus.Encode(opusWriter, sourceChannels, logger)
	if err != nil {
		return nil, err
	}

	// resampled writer resamples the PCM16 samples from sourceSampleRate to DefaultOpusSampleRate
	// and writes them to pcmWriter. If no resampling is needed, we directly point resampledPCMWriter to pcmWriter.
	resampledPCMWriter := pcmWriter
	if sourceSampleRate != DefaultOpusSampleRate {
		resampledPCMWriter = media.ResampleWriter(pcmWriter, sourceSampleRate)
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
		frameDuration:          defaultPCMSampleDuration,
		sourceChannels:         sourceChannels,
		chunkBuffer:            buffer.NewCircularBuffer[int16](defaultBufferCapacity),
		chunksPerSample:        (sourceSampleRate * sourceChannels * int(defaultPCMSampleDuration/time.Nanosecond)) / 1e9,
		writeSilenceOnNoData:   params.WriteSilenceOnNoData,
	}

	t.writeBufCond = sync.NewCond(&t.writeBufMu)
	t.emptyBufCond = sync.NewCond(&t.emptyBufMu)
	go t.processSamples()
	return t, nil
}

func (t *PCMLocalTrack) waitUntilBufferHasChunks(count int) bool {
	t.writeBufMu.Lock()
	defer t.writeBufMu.Unlock()

	didWait := false
	for int(t.chunkBuffer.Size()) < count && !t.closed.Load() {
		t.emptyBufMu.Lock()
		t.emptyBufCond.Broadcast()
		t.emptyBufMu.Unlock()

		didWait = true
		t.writeBufCond.Wait()
	}

	return didWait
}

func (t *PCMLocalTrack) getChunksFromBuffer() (media.PCM16Sample, bool) {
	var didWait = false

	if !t.writeSilenceOnNoData {
		didWait = t.waitUntilBufferHasChunks(t.chunksPerSample)
	}

	if t.closed.Load() && t.chunkBuffer.IsEmpty() {
		return nil, false
	}

	_, items := t.chunkBuffer.PopBatch(t.chunksPerSample)
	chunks := make(media.PCM16Sample, t.chunksPerSample)
	copy(chunks, items)

	return chunks, didWait
}

func (t *PCMLocalTrack) WriteSample(sample media.PCM16Sample) error {
	if t.closed.Load() {
		return errors.New("track is closed")
	}

	t.chunkBuffer.PushBatchBlocking(sample)

	// Signal waiting goroutines that new samples are available
	t.writeBufMu.Lock()
	t.writeBufCond.Broadcast()
	t.writeBufMu.Unlock()

	return nil
}

func (t *PCMLocalTrack) processSamples() {
	ticker := time.NewTicker(t.frameDuration)
	defer ticker.Stop()

	for {
		if t.closed.Load() && int(t.chunkBuffer.Size()) > t.chunksPerSample {
			break
		}

		sample, didWait := t.getChunksFromBuffer()
		if sample != nil {
			// sample is only nil when the track is closed, so we don't need to
			// adjust ticker for this case.
			t.resampledPCMWriter.WriteSample(sample)
			if didWait {
				ticker.Reset(t.frameDuration)
			}
		}
		<-ticker.C
	}

	t.resampledPCMWriter.Close()
	t.pcmWriter.Close()
	t.opusWriter.Close()
}

func (t *PCMLocalTrack) WaitForPlayout() {
	t.emptyBufMu.Lock()
	defer t.emptyBufMu.Unlock()

	for t.chunkBuffer.Size() > uint32(t.chunksPerSample) {
		t.emptyBufCond.Wait()
	}
}

func (t *PCMLocalTrack) ClearQueue() {
	t.chunkBuffer.Clear()

	t.emptyBufMu.Lock()
	t.emptyBufCond.Broadcast()
	t.emptyBufMu.Unlock()
}

func (t *PCMLocalTrack) Close() {
	if t.closed.CompareAndSwap(false, true) {
		t.writeBufMu.Lock()
		t.writeBufCond.Broadcast()
		t.writeBufMu.Unlock()

		t.emptyBufMu.Lock()
		t.emptyBufCond.Broadcast()
		t.emptyBufMu.Unlock()
	}
}

type PCMRemoteTrackWriter interface {
	WriteSample(sample media.PCM16Sample) error
	Close() error
}

type internalPCMRemoteTrackWriter struct {
	PCMRemoteTrackWriter
	sampleRate int
}

func (w *internalPCMRemoteTrackWriter) SampleRate() int {
	return w.sampleRate
}

func (w *internalPCMRemoteTrackWriter) String() string {
	return fmt.Sprintf("PCMRemoteTrackWriter(%d)", w.sampleRate)
}

type PCMRemoteTrackParams struct {
	HandleJitter     bool
	TargetSampleRate int
	TargetChannels   int
}

type PCMRemoteTrackOption func(*PCMRemoteTrackParams)

func WithHandleJitter(handleJitter bool) PCMRemoteTrackOption {
	return func(p *PCMRemoteTrackParams) {
		p.HandleJitter = handleJitter
	}
}

func WithTargetSampleRate(targetSampleRate int) PCMRemoteTrackOption {
	return func(p *PCMRemoteTrackParams) {
		p.TargetSampleRate = targetSampleRate
	}
}

func WithTargetChannels(targetChannels int) PCMRemoteTrackOption {
	return func(p *PCMRemoteTrackParams) {
		p.TargetChannels = targetChannels
	}
}

type PCMRemoteTrack struct {
	trackRemote *webrtc.TrackRemote
	channels    int
	sampleRate  int
	isResampled bool

	opusWriter         media.WriteCloser[opus.Sample]
	pcmMWriter         media.WriteCloser[media.PCM16Sample]
	resampledPCMWriter media.WriteCloser[media.PCM16Sample]
	logger             protoLogger.Logger
}

// PCMRemoteTrack takes a remote track (currently only opus is supported)
// and a WriterCloser interface that writes implements a WriteSample method to write PCM16 samples, where the user desires.
// The PCMRemoteTrack will read RTP packets from the remote track, decode them to PCM16 samples, and write them to the writer.
// Audio is resampled to targetSampleRate and upmixed/downmixed to targetChannels.
// It also provides an option to handle jitter, which is enabled by default.
// Stereo remote tracks are currently not supported, and are known to have a lot of unpleasant noise.
func NewPCMRemoteTrack(track *webrtc.TrackRemote, writer PCMRemoteTrackWriter, opts ...PCMRemoteTrackOption) (*PCMRemoteTrack, error) {
	if track.Codec().MimeType != webrtc.MimeTypeOpus {
		return nil, errors.New("track is not opus")
	}

	options := &PCMRemoteTrackParams{
		HandleJitter:     true,
		TargetSampleRate: DefaultOpusSampleRate,
		TargetChannels:   1,
	}
	for _, opt := range opts {
		opt(options)
	}

	targetChannels := options.TargetChannels
	targetSampleRate := options.TargetSampleRate
	if targetChannels <= 0 || targetChannels > 2 || targetSampleRate <= 0 {
		return nil, errors.New("invalid target channels or sample rate")
	}

	internalWriter := &internalPCMRemoteTrackWriter{
		PCMRemoteTrackWriter: writer,
		sampleRate:           targetSampleRate,
	}

	// resampledPCMWriter resamples the PCM16 samples from DefaultOpusSampleRate to targetSampleRate and
	// writes them to the writer. If no resampling is needed, we directly point resampledPCMWriter to writer.
	var isResampled bool
	var resampledPCMWriter media.WriteCloser[media.PCM16Sample]
	if targetSampleRate != DefaultOpusSampleRate {
		resampledPCMWriter = media.ResampleWriter(internalWriter, DefaultOpusSampleRate)
		isResampled = true
	} else {
		resampledPCMWriter = internalWriter
	}

	// opus writer takes opus samples, decodes them to PCM16 samples
	// and writes them to the pcmMWriter
	opusWriter, err := opus.Decode(resampledPCMWriter, targetChannels, protoLogger.GetLogger())
	if err != nil {
		return nil, err
	}

	// the final chain of writers:
	// trackRemote -> rtp handlers (reads RTP packets from the track) -> opusWriter (decodes opus -> PCM16) -> resampledPCMWriter (resamples to target sample rate as necessary)
	// -> user provided writer (writes the final PCM16 samples)
	t := &PCMRemoteTrack{
		trackRemote:        track,
		opusWriter:         opusWriter,
		pcmMWriter:         internalWriter,
		resampledPCMWriter: resampledPCMWriter,
		sampleRate:         targetSampleRate,
		channels:           targetChannels,
		logger:             protoLogger.GetLogger(),
		isResampled:        isResampled,
	}

	go t.process(options.HandleJitter)
	return t, nil
}

func (t *PCMRemoteTrack) process(handleJitter bool) {
	// Handler takes RTP packets and writes the payload to opusWriter
	var h rtp.Handler = rtp.NewMediaStreamIn[opus.Sample](t.opusWriter)
	if handleJitter {
		h = rtp.HandleJitter(h)
	}

	// HandleLoop takes RTP packets from the track and writes them to the handler
	// TODO(anunaym14): handle concealment
	err := rtp.HandleLoop(t.trackRemote, h)
	if err != nil && !errors.Is(err, io.EOF) {
		t.logger.Errorw("error handling rtp from track", err)
	}
}

func (t *PCMRemoteTrack) Close() {
	if t.isResampled {
		t.pcmMWriter.Close()
	}
	// opus writer closes resampledPCMWriter internally
	t.opusWriter.Close()
}
