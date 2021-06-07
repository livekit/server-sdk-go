package lksdk

import (
	"encoding/binary"
	"errors"
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

type LoadTestProvider struct {
	BytesPerSample uint32
	SampleDuration time.Duration
	count          byte
}

func NewLoadTestProvider(bitrate uint32) (*LoadTestProvider, error) {
	bps := bitrate / 8 / 30

	// needs at least 9 bytes
	if bps < 9 {
		return nil, errors.New("minimum bitrate 2160")
	}

	return &LoadTestProvider{
		SampleDuration: time.Second / 30,
		BytesPerSample: bps,
		count:          byte(0),
	}, nil
}

func (p *LoadTestProvider) NextSample() (media.Sample, error) {
	packet := make([]byte, p.BytesPerSample)
	binary.LittleEndian.PutUint64(packet, uint64(time.Now().UnixNano()))
	packet[len(packet)-1] = p.count
	p.count += byte(1)

	return media.Sample{
		Data:     packet,
		Duration: p.SampleDuration,
	}, nil
}

func DecodeLoadTestPacket(packet []byte) (int64, int) {
	count := int(packet[len(packet)-1])
	unixNano := int64(binary.LittleEndian.Uint64(packet[:8]))
	return unixNano, count
}
