package lksdk

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gammazero/deque"
	audio "github.com/livekit/mediatransportutil/pkg/audio"
	opus "github.com/livekit/mediatransportutil/pkg/audio/opus"
	rtp "github.com/livekit/mediatransportutil/pkg/audio/rtp"
	protoLogger "github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"
)

const (
	opusSampleRate = 48000
)

type PCM16ToOpusAudioTrack struct {
	*webrtc.TrackLocalStaticSample

	opusWriter         audio.WriteCloser[opus.Sample]
	pcmWriter          audio.WriteCloser[audio.PCM16Sample]
	resampledPCMWriter audio.WriteCloser[audio.PCM16Sample]

	frameDuration time.Duration

	sampleBuffer *deque.Deque[audio.PCM16Sample]
	started      sync.Once
	ticker       *time.Ticker

	closed atomic.Bool
	mu     sync.Mutex
	cond   *sync.Cond
}

// TODO: Support stereo
func NewPCM16ToOpusAudioTrack(sampleRate int, frameDuration time.Duration, logger protoLogger.Logger) (*PCM16ToOpusAudioTrack, error) {
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "test", "test")
	if err != nil {
		return nil, err
	}

	opusWriter := audio.FromSampleWriter[opus.Sample](track, opusSampleRate, frameDuration)
	pcmWriter, err := opus.Encode(opusWriter, 1, logger)
	if err != nil {
		return nil, err
	}

	resampledPCMWriter := pcmWriter
	if sampleRate != opusSampleRate {
		resampledPCMWriter = audio.ResampleWriter(pcmWriter, opusSampleRate)
	}

	t := &PCM16ToOpusAudioTrack{
		TrackLocalStaticSample: track,
		opusWriter:             opusWriter,
		pcmWriter:              pcmWriter,
		resampledPCMWriter:     resampledPCMWriter,
		frameDuration:          frameDuration,
		sampleBuffer:           new(deque.Deque[audio.PCM16Sample]),
	}
	t.cond = sync.NewCond(&t.mu)

	go t.processSamples()
	return t, nil
}

func (t *PCM16ToOpusAudioTrack) WriteSample(sample audio.PCM16Sample) error {
	if t.closed.Load() {
		return errors.New("track is closed")
	}

	t.mu.Lock()
	t.sampleBuffer.PushBack(sample)
	t.cond.Broadcast()
	t.mu.Unlock()
	return nil
}

func (t *PCM16ToOpusAudioTrack) waitForSamples() {
	t.mu.Lock()
	for t.sampleBuffer.Len() == 0 && !t.closed.Load() {
		t.cond.Wait()
	}
	t.mu.Unlock()
}

func (t *PCM16ToOpusAudioTrack) processSamples() {
	// wait for the first sample before starting the ticker
	t.waitForSamples()
	t.resampledPCMWriter.WriteSample(t.sampleBuffer.PopFront())
	t.ticker = time.NewTicker(t.frameDuration)

	for range t.ticker.C {
		isBufferEmpty := t.sampleBuffer.Len() == 0
		if t.closed.Load() {
			return
		}
		if isBufferEmpty {
			t.waitForSamples()
		}

		t.mu.Lock()
		sample := t.sampleBuffer.PopFront()

		if isBufferEmpty {
			t.ticker.Reset(t.frameDuration)
		}

		t.resampledPCMWriter.WriteSample(sample)
		t.mu.Unlock()
	}

	t.sampleBuffer.Clear()
	t.ticker.Stop()
}

func (t *PCM16ToOpusAudioTrack) Close() {
	t.closed.Store(true)
	t.cond.Broadcast()

	t.resampledPCMWriter.Close()
	t.pcmWriter.Close()
	t.opusWriter.Close()
}

type OpusToPCM16AudioTrack struct {
	*webrtc.TrackRemote
	channels   int
	sampleRate int

	opusWriter audio.WriteCloser[opus.Sample]
	pcmMWriter audio.WriteCloser[audio.PCM16Sample]
	logger     protoLogger.Logger
}

// TODO: We also have a reader API, but writer is more efficient as it is zero copy.
// Shall we support both reader and writer?
// Reader makes more sense while reading the code, might be easier for the end user to understand.
// But, it's less efficient as it involves a copy.
func NewOpusToPCM16AudioTrack(track *webrtc.TrackRemote, publication *RemoteTrackPublication, writer *audio.WriteCloser[audio.PCM16Sample], sampleRate int, handleJitter bool) (*OpusToPCM16AudioTrack, error) {
	if track.Codec().MimeType != webrtc.MimeTypeOpus {
		return nil, errors.New("track is not opus")
	}

	channels := 1
	if publication.TrackInfo().Stereo {
		channels = 2
	}

	resampledPCMWriter := *writer
	if sampleRate != opusSampleRate {
		resampledPCMWriter = audio.ResampleWriter(*writer, sampleRate)
	}

	opusWriter, err := opus.Decode(resampledPCMWriter, channels, protoLogger.GetLogger())
	if err != nil {
		return nil, err
	}

	t := &OpusToPCM16AudioTrack{TrackRemote: track, opusWriter: opusWriter, pcmMWriter: resampledPCMWriter, channels: channels, sampleRate: sampleRate, logger: protoLogger.GetLogger()}
	go t.process(handleJitter)
	return t, nil
}

func (t *OpusToPCM16AudioTrack) process(handleJitter bool) {
	var h rtp.Handler = rtp.NewMediaStreamIn[opus.Sample](t.opusWriter)
	if handleJitter {
		h = rtp.HandleJitter(int(t.TrackRemote.Codec().ClockRate), h)
	}
	err := rtp.HandleLoop(t.TrackRemote, h)
	if err != nil && !errors.Is(err, io.EOF) {
		t.logger.Errorw("error handling rtp from track", err)
	}
}

func (t *OpusToPCM16AudioTrack) Channels() int {
	return t.channels
}

func (t *OpusToPCM16AudioTrack) SampleRate() int {
	return t.sampleRate
}

func (t *OpusToPCM16AudioTrack) Close() {
	// opus writer closes resampledPCMWriter internally
	t.opusWriter.Close()
}
