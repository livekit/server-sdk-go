package lksdk

import (
	"time"

	"github.com/pion/webrtc/v3/pkg/media"
)

type SampleProvider interface {
	NextSample() (media.Sample, error)
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

type CountingSampleProvider struct {
	BytesPerSample uint32
	SampleDuration time.Duration
	lastSent       []byte
}

func NewCountingSampleProvider(bitrate uint32) *CountingSampleProvider {
	bps := bitrate / 8 / 30
	return &CountingSampleProvider{
		SampleDuration: time.Second / 30,
		BytesPerSample: bps,
		lastSent:       make([]byte, bps),
	}
}

func (p *CountingSampleProvider) NextSample() (media.Sample, error) {
	next := make([]byte, len(p.lastSent))
	copy(next, p.lastSent)
	for i := len(next) - 1; i >= 0; i-- {
		next[i] += byte(1)
		if next[i] != byte(0) {
			break
		}
	}
	p.lastSent = next
	return media.Sample{
		Data:     next,
		Duration: p.SampleDuration,
	}, nil
}
