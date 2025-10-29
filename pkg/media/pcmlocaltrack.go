package media

import (
	"errors"
	"sync"
	"time"

	"github.com/gammazero/deque"
	"github.com/google/uuid"
	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/opus"
	protoLogger "github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

type PCMLocalTrackParams struct {
	Encryptor Encryptor
}

type PCMLocalTrackOption func(*PCMLocalTrackParams)

func WithEncryptor(encryptor Encryptor) PCMLocalTrackOption {
	return func(p *PCMLocalTrackParams) {
		p.Encryptor = encryptor
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
	samplesPerFrame  int

	// int16 to support a LE/BE PCM16 chunk that has a high byte and low byte
	// TODO(anunaym14): switch out deque for a ring buffer
	chunkBuffer *deque.Deque[media.PCM16Sample]

	mu   sync.Mutex
	cond *sync.Cond

	emptyBufMu   sync.Mutex
	emptyBufCond *sync.Cond

	closed atomic.Bool
	muted  atomic.Bool
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

	// opusWriter writes opus samples to the track
	var opusWriter media.WriteCloser[opus.Sample]
	if params.Encryptor != nil {
		encryptionHandler := newEncryptionHandler(track, params.Encryptor, sourceSampleRate)
		opusWriter = media.FromSampleWriter[opus.Sample](encryptionHandler, sourceSampleRate, defaultPCMFrameDuration)
	} else {
		opusWriter = media.FromSampleWriter[opus.Sample](track, DefaultOpusSampleRate, defaultPCMFrameDuration)
	}
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
		frameDuration:          defaultPCMFrameDuration,
		sourceChannels:         sourceChannels,
		chunkBuffer:            new(deque.Deque[media.PCM16Sample]),
		samplesPerFrame:        (sourceSampleRate * sourceChannels * int(defaultPCMFrameDuration/time.Nanosecond)) / 1e9,
	}

	t.cond = sync.NewCond(&t.mu)
	t.emptyBufCond = sync.NewCond(&t.emptyBufMu)
	go t.processSamples()
	return t, nil
}

func (t *PCMLocalTrack) getFrameFromChunkBuffer() media.PCM16Sample {
	if t.closed.Load() && t.getNumSamplesInChunkBuffer() == 0 {
		return nil
	}

	frame := make(media.PCM16Sample, 0, t.samplesPerFrame)
	for len(frame) < t.samplesPerFrame && t.chunkBuffer.Len() != 0 {
		chunk := t.chunkBuffer.PopFront()
		remaining := min(t.samplesPerFrame-len(frame), len(chunk))
		frame = append(frame, chunk[:remaining]...)
		if remaining < len(chunk) {
			t.chunkBuffer.PushFront(chunk[remaining:])
		}
	}

	if len(frame) < t.samplesPerFrame {
		frame = append(frame, make(media.PCM16Sample, t.samplesPerFrame-len(frame))...)
	}

	if t.chunkBuffer.Len() == 0 {
		t.emptyBufMu.Lock()
		t.emptyBufCond.Broadcast()
		t.emptyBufMu.Unlock()
	}

	return frame
}

func (t *PCMLocalTrack) getNumSamplesInChunkBuffer() int {
	numSamples := 0
	for i := 0; i < t.chunkBuffer.Len(); i++ {
		numSamples += len(t.chunkBuffer.At(i))
	}
	return numSamples
}

func (t *PCMLocalTrack) WriteSample(chunk media.PCM16Sample) error {
	if t.closed.Load() {
		return errors.New("track is closed")
	}

	if t.muted.Load() || len(chunk) == 0 {
		return nil
	}

	chunkCopy := make(media.PCM16Sample, len(chunk))
	copy(chunkCopy, chunk)

	t.mu.Lock()
	t.chunkBuffer.PushBack(chunkCopy)
	t.cond.Broadcast()
	t.mu.Unlock()
	return nil
}

func (t *PCMLocalTrack) processSamples() {
	ticker := time.NewTicker(t.frameDuration)
	defer ticker.Stop()

	for {
		if t.closed.Load() && t.getNumSamplesInChunkBuffer() == 0 {
			break
		}

		t.mu.Lock()
		frame := t.getFrameFromChunkBuffer()
		if frame != nil {
			t.resampledPCMWriter.WriteSample(frame)
		}
		t.mu.Unlock()

		<-ticker.C
	}

	// closing the writers here because we continue to write on close
	// until the buffer is empty
	t.resampledPCMWriter.Close()
	t.pcmWriter.Close()
	t.opusWriter.Close()
}

func (t *PCMLocalTrack) setMuted(muted bool) error {
	if t.closed.Load() {
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

	for t.getNumSamplesInChunkBuffer() > 0 {
		t.emptyBufCond.Wait()
	}
}

func (t *PCMLocalTrack) ClearQueue() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.chunkBuffer.Clear()

	t.emptyBufMu.Lock()
	t.emptyBufCond.Broadcast()
	t.emptyBufMu.Unlock()
}

func (t *PCMLocalTrack) Close() error {
	if t.closed.CompareAndSwap(false, true) {
		t.mu.Lock()
		t.cond.Broadcast()
		t.mu.Unlock()
	}
	return nil
}

func (t *PCMLocalTrack) SampleRate() int {
	return t.sourceSampleRate
}

func (t *PCMLocalTrack) String() string {
	return "PCMLocalTrack"
}
