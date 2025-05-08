// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package jitter

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

const testBufferLatency = 800 * time.Millisecond

func TestJitterBuffer(t *testing.T) {
	out := make(chan []*rtp.Packet, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, out, nil)
	s := newTestStream()

	i := 0
	for ; i < 100; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  100,
		PacketsLost:    0,
		PacketsDropped: 0,
		PacketsPopped:  100,
		SamplesPopped:  100,
	})
}

func TestSamples(t *testing.T) {
	out := make(chan []*rtp.Packet, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, out, nil)
	s := newTestStream()

	i := 0
	for ; i < 50; i++ {
		b.Push(s.gen(true, false))
		checkSample(t, out, 0)

		b.Push(s.gen(false, true))
		checkSample(t, out, 2)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  100,
		PacketsLost:    0,
		PacketsDropped: 0,
		PacketsPopped:  100,
		SamplesPopped:  50,
	})
}

func TestJitter(t *testing.T) {
	out := make(chan []*rtp.Packet, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, out, nil)
	s := newTestStream()

	i := 0
	for ; i < 17; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}

	ooo := []*rtp.Packet{
		s.gen(true, true),
		s.gen(true, true),
		s.gen(true, true),
	}
	b.Push(ooo[1])
	b.Push(ooo[2])
	checkSample(t, out, 0)

	b.Push(ooo[0])
	checkSample(t, out, 1)
	checkSample(t, out, 1)
	checkSample(t, out, 1)

	checkStats(t, b, &BufferStats{
		PacketsPushed:  20,
		PacketsLost:    0,
		PacketsDropped: 0,
		PacketsPopped:  20,
		SamplesPopped:  20,
	})
}

func TestDiscontinuity(t *testing.T) {
	out := make(chan []*rtp.Packet, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, out, nil)
	s := newTestStream()

	i := 0
	for ; i < 50; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}
	s.discont()
	for ; i < 100; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  100,
		PacketsLost:    0,
		PacketsDropped: 0,
		PacketsPopped:  100,
		SamplesPopped:  100,
	})
}

func TestLostPackets(t *testing.T) {
	out := make(chan []*rtp.Packet, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, out, nil)
	s := newTestStream()

	i := 0
	for ; i < 10; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}

	// packet loss
	_ = s.gen(true, true)

	for ; i < 20; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 0)
	}

	// latency
	time.Sleep(time.Second)
	for range 10 {
		checkSample(t, out, 1)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  20,
		PacketsLost:    1,
		PacketsDropped: 0,
		PacketsPopped:  20,
		SamplesPopped:  20,
	})
}

func TestDroppedPackets(t *testing.T) {
	out := make(chan []*rtp.Packet, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, out, nil)
	s := newTestStream()

	i := 0
	for ; i < 10; i++ {
		b.Push(s.gen(true, false))
		b.Push(s.gen(false, true))
		checkSample(t, out, 2)
	}

	// packet loss - missing head
	_ = s.gen(true, false)
	b.Push(s.gen(false, true))
	checkSample(t, out, 0)

	for ; i < 20; i++ {
		b.Push(s.gen(true, false))
		b.Push(s.gen(false, true))
		checkSample(t, out, 0)
	}

	time.Sleep(time.Millisecond * 500)

	// packet loss - missing tail
	b.Push(s.gen(true, false))
	_ = s.gen(false, true)
	checkSample(t, out, 0)

	for ; i < 30; i++ {
		b.Push(s.gen(true, false))
		b.Push(s.gen(false, true))
		checkSample(t, out, 0)
	}

	time.Sleep(time.Millisecond * 500)

	// first incomplete sample expired
	for range 10 {
		checkSample(t, out, 2)
	}
	checkSample(t, out, 0)

	time.Sleep(time.Millisecond * 500)

	// second incomplete sample expired
	for range 10 {
		checkSample(t, out, 2)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  62,
		PacketsLost:    2,
		PacketsDropped: 2,
		PacketsPopped:  60,
		SamplesPopped:  30,
	})
}

func checkSample(t *testing.T, out chan []*rtp.Packet, expected int) {
	select {
	case sample := <-out:
		if expected == 0 {
			t.Fatal("received unexpected sample")
		} else {
			require.Equal(t, expected, len(sample))
		}
	default:
		if expected > 0 {
			t.Fatal("expected to receive sample")
		}
	}
}

func checkStats(t *testing.T, b *Buffer, expected *BufferStats) {
	stats := b.Stats()
	require.Equal(t, expected.PacketsPushed, stats.PacketsPushed)
	require.Equal(t, expected.PacketsLost, stats.PacketsLost)
	require.Equal(t, expected.PacketsDropped, stats.PacketsDropped)
	require.Equal(t, expected.PacketsPopped, stats.PacketsPopped)
	require.Equal(t, expected.SamplesPopped, stats.SamplesPopped)
}

type stream struct {
	seq uint16
}

func newTestStream() *stream {
	return &stream{
		seq: uint16(rand.Uint32()),
	}
}

func (s *stream) gen(head, tail bool) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			Marker:         tail,
			SequenceNumber: s.seq,
		},
		Payload: make([]byte, defaultPacketSize),
	}
	if head {
		copy(p.Payload, headerBytes)
	}
	s.seq++
	return p
}

func (s *stream) discont() {
	s.seq += math.MaxUint16 / 2
}

const defaultPacketSize = 200

var headerBytes = []byte{0xaa, 0xaa}

type testDepacketizer struct{}

func (d *testDepacketizer) Unmarshal(r []byte) ([]byte, error) {
	return r, nil
}

func (d *testDepacketizer) IsPartitionHead(payload []byte) bool {
	if headerBytes == nil || len(payload) < len(headerBytes) {
		return false
	}
	for i, b := range headerBytes {
		if payload[i] != b {
			return false
		}
	}
	return true
}

func (d *testDepacketizer) IsPartitionTail(marker bool, _ []byte) bool {
	return marker
}
