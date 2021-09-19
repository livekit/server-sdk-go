package lksdk

import (
	"encoding/binary"
	"errors"
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

// LoadTestProvider is designed to be used with the load tester.
// It provides samples that are encoded with sequence and timing information, in order determine RTT and loss
type LoadTestProvider struct {
	BytesPerSample uint32
	SampleDuration time.Duration
}

func NewLoadTestProvider(bitrate uint32) (*LoadTestProvider, error) {
	bps := bitrate / 8 / 30
	if bps < 8 {
		return nil, errors.New("bitrate lower than minimum of 1920")
	}

	return &LoadTestProvider{
		SampleDuration: time.Second / 30,
		BytesPerSample: bps,
	}, nil
}

func (p *LoadTestProvider) NextSample() (media.Sample, error) {
	ts := make([]byte, 8)
	binary.LittleEndian.PutUint64(ts, uint64(time.Now().UnixNano()))
	packet := append(make([]byte, p.BytesPerSample-8), ts...)

	return media.Sample{
		Data:     packet,
		Duration: p.SampleDuration,
	}, nil
}

func (p *LoadTestProvider) OnBind() error {
	return nil
}

func (p *LoadTestProvider) OnUnbind() error {
	return nil
}
