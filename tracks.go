package lksdk

import (
	"io"
	"time"

	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/utils"
	"github.com/pion/webrtc/v3"
)

type Track interface {
	ID() string
}

type TrackKind string

const (
	TrackKindVideo TrackKind = "video"
	TrackKindAudio TrackKind = "audio"
)

func (k TrackKind) String() string {
	return string(k)
}

func (k TrackKind) RTPType() webrtc.RTPCodecType {
	return webrtc.NewRTPCodecType(k.String())
}

func (k TrackKind) ProtoType() livekit.TrackType {
	switch k {
	case TrackKindAudio:
		return livekit.TrackType_AUDIO
	case TrackKindVideo:
		return livekit.TrackType_VIDEO
	}
	return livekit.TrackType(0)
}

func KindFromRTPType(rt webrtc.RTPCodecType) TrackKind {
	return TrackKind(rt.String())
}

// LocalSampleTrack is a local track that simplifies writing samples.
// It handles timing and publishing of things, so as long as a SampleProvider is provided, the class takes care of
// publishing tracks at the right frequency
type LocalSampleTrack struct {
	webrtc.TrackLocalStaticSample
	provider SampleProvider
	closed   chan struct{}
}

func NewLocalSampleTrack(c webrtc.RTPCodecCapability, sampleProvider SampleProvider) (*LocalSampleTrack, error) {
	sample, err := webrtc.NewTrackLocalStaticSample(c, utils.NewGuid("TR_"), utils.NewGuid("ST_"))
	if err != nil {
		return nil, err
	}
	return &LocalSampleTrack{
		TrackLocalStaticSample: *sample,
		provider:               sampleProvider,
	}, nil
}

func (s *LocalSampleTrack) Bind(t webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	params, err := s.TrackLocalStaticSample.Bind(t)
	if err == nil {
		err = s.provider.OnBind()
	}
	if err == nil {
		s.closed = make(chan struct{})
		// start the writing process
		go s.writeWorker()
	}
	return params, err
}

func (s *LocalSampleTrack) Unbind(t webrtc.TrackLocalContext) error {
	if s.closed != nil {
		close(s.closed)
	}
	err := s.provider.OnUnbind()
	unbindErr := s.TrackLocalStaticSample.Unbind(t)
	if unbindErr != nil {
		return unbindErr
	}
	return err
}

func (s *LocalSampleTrack) writeWorker() {
	nextSampleTime := time.Now()
	ticker := time.NewTicker(10 * time.Millisecond)
	for {
		sample, err := s.provider.NextSample()
		if err == io.EOF {
			return
		}
		if err != nil {
			logger.Error(err, "could not get sample from provider")
			return
		}
		if sample.Duration == 0 {
			logger.Error(err, "sample duration not set")
			return
		}

		if err := s.TrackLocalStaticSample.WriteSample(sample); err != nil {
			logger.Error(err, "could not write sample")
			return
		}
		nextSampleTime = nextSampleTime.Add(sample.Duration)
		sleepDuration := nextSampleTime.Sub(time.Now())
		if sleepDuration < 0 {
			continue
		}
		ticker.Reset(sleepDuration)

		select {
		case <-ticker.C:
			continue
		case <-s.closed:
			return
		}
	}
}
