package lksdk

import (
	"errors"
	"io"
	"sync"
	"time"

	audio "github.com/livekit/mediatransportutil/pkg/audio"
	opus "github.com/livekit/mediatransportutil/pkg/audio/opus"
	rtp "github.com/livekit/mediatransportutil/pkg/audio/rtp"
	protoLogger "github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
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

	sampleBuffer []audio.PCM16Sample
	started      sync.Once
	ticker       *time.Ticker
	mu           sync.Mutex
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

	return &PCM16ToOpusAudioTrack{
		TrackLocalStaticSample: track,
		opusWriter:             opusWriter,
		pcmWriter:              pcmWriter,
		resampledPCMWriter:     resampledPCMWriter,
		frameDuration:          frameDuration,
		// TODO: Maybe not the best thing to do, good for a PoC
		sampleBuffer: make([]audio.PCM16Sample, 0),
	}, nil
}

func (t *PCM16ToOpusAudioTrack) WriteSample(sample audio.PCM16Sample) error {
	var isFirstSample bool
	var err error

	t.started.Do(func() {
		isFirstSample = true
		// write the first sample immediately before starting the ticker
		err = t.resampledPCMWriter.WriteSample(sample)
		t.ticker = time.NewTicker(t.frameDuration)
		go t.processSamples()
	})

	if isFirstSample {
		return err
	}

	t.mu.Lock()
	t.sampleBuffer = append(t.sampleBuffer, sample)
	t.mu.Unlock()
	return nil
}

func (t *PCM16ToOpusAudioTrack) processSamples() {
	for range t.ticker.C {
		t.mu.Lock()
		if len(t.sampleBuffer) == 0 {
			for {
				if len(t.sampleBuffer) > 0 {
					break
				}
				time.Sleep(time.Millisecond * 10)
			}
			// TODO: fix: ticker would have to be reset to the point where it gets the next sample on an empty buffer
			t.mu.Unlock()
			continue
		}
		sample := t.sampleBuffer[0]
		if len(t.sampleBuffer) > 1 {
			t.sampleBuffer = t.sampleBuffer[1:]
		} else {
			t.sampleBuffer = make([]audio.PCM16Sample, 0)
		}
		t.mu.Unlock()
		t.resampledPCMWriter.WriteSample(sample)
	}
}

func (t *PCM16ToOpusAudioTrack) Close() {
	t.ticker.Stop()
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
