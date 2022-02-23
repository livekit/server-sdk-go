package lksdk

import (
	"time"

	"github.com/pion/webrtc/v3/pkg/media"
)

type SampleProvider interface {
	NextSample() (media.Sample, error)
	OnBind() error
	OnUnbind() error
}

type AudioSampleProvider interface {
	SampleProvider
	CurrentAudioLevel() uint8
}

// NullSampleProvider is a media provider that provides null packets, it could meet a certain bitrate, if desired
type NullSampleProvider struct {
	BytesPerSample uint32
	SampleDuration time.Duration
}

func NewNullSampleProvider(bitrate uint32) *NullSampleProvider {
	return &NullSampleProvider{
		SampleDuration: time.Second / 30,
		BytesPerSample: bitrate / 8 / 30,
	}
}

func (p *NullSampleProvider) NextSample() (media.Sample, error) {
	return media.Sample{
		Data:     make([]byte, p.BytesPerSample),
		Duration: p.SampleDuration,
	}, nil
}

func (p *NullSampleProvider) OnBind() error {
	return nil
}

func (p *NullSampleProvider) OnUnbind() error {
	return nil
}
