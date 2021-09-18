package lksdk

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
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
	bound uint32
	lock  sync.Mutex

	cancelWrite func()
	provider    SampleProvider
	onBind      func()
	onUnbind    func()
	// notify when sample provider responds with EOF
	onWriteComplete func()
}

func NewLocalSampleTrack(c webrtc.RTPCodecCapability) (*LocalSampleTrack, error) {
	sample, err := webrtc.NewTrackLocalStaticSample(c, utils.NewGuid("TR_"), utils.NewGuid("ST_"))
	if err != nil {
		return nil, err
	}
	return &LocalSampleTrack{
		TrackLocalStaticSample: *sample,
	}, nil
}

func (s *LocalSampleTrack) IsBound() bool {
	return atomic.LoadUint32(&s.bound) == 1
}

// Bind is an interface for TrackLocal, not for external consumption
func (s *LocalSampleTrack) Bind(t webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	params, err := s.TrackLocalStaticSample.Bind(t)
	if err != nil {
		return params, err
	}
	s.lock.Lock()
	onBind := s.onBind
	provider := s.provider
	onWriteComplete := s.onWriteComplete
	atomic.StoreUint32(&s.bound, 1)
	s.lock.Unlock()

	if provider != nil {
		err = provider.OnBind()
		go s.writeWorker(provider, onWriteComplete)
	}

	// notify callbacks last
	if onBind != nil {
		go onBind()
	}
	return params, err
}

// Unbind is an interface for TrackLocal, not for external consumption
func (s *LocalSampleTrack) Unbind(t webrtc.TrackLocalContext) error {
	s.lock.Lock()
	provider := s.provider
	onUnbind := s.onUnbind
	atomic.StoreUint32(&s.bound, 0)
	cancel := s.cancelWrite
	s.lock.Unlock()

	var err error

	if provider != nil {
		err = provider.OnUnbind()
	}
	if cancel != nil {
		cancel()
	}
	if onUnbind != nil {
		go onUnbind()
	}
	unbindErr := s.TrackLocalStaticSample.Unbind(t)
	if unbindErr != nil {
		return unbindErr
	}
	return err
}

func (s *LocalSampleTrack) StartWrite(provider SampleProvider, onComplete func()) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.provider == provider {
		return nil
	}

	// when bound and already writing, ignore
	if s.IsBound() {
		// unbind previous provider
		if s.provider != nil {
			if err := s.provider.OnUnbind(); err != nil {
				return err
			}
		}
		if err := provider.OnBind(); err != nil {
			return err
		}
		// start new writer
		go s.writeWorker(provider, onComplete)
	}
	s.provider = provider
	s.onWriteComplete = onComplete
	return nil
}

// OnBind sets a callback to be called when the track has been negotiated for publishing and bound to a peer connection
func (s *LocalSampleTrack) OnBind(f func()) {
	s.lock.Lock()
	s.onBind = f
	s.lock.Unlock()
}

// OnUnbind sets a callback to be called after the track is removed from a peer connection
func (s *LocalSampleTrack) OnUnbind(f func()) {
	s.lock.Lock()
	s.onUnbind = f
	s.lock.Unlock()
}

func (s *LocalSampleTrack) writeWorker(provider SampleProvider, onComplete func()) {
	if s.cancelWrite != nil {
		s.cancelWrite()
	}
	var ctx context.Context
	s.lock.Lock()
	ctx, s.cancelWrite = context.WithCancel(context.Background())
	s.lock.Unlock()
	if onComplete != nil {
		defer onComplete()
	}

	nextSampleTime := time.Now()
	ticker := time.NewTicker(10 * time.Millisecond)
	for {
		sample, err := provider.NextSample()
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
		case <-ctx.Done():
			return
		}
	}
}
