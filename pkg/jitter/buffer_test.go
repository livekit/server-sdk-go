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
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

func TestJitterBuffer(t *testing.T) {
	onPacketDroppedCalled := 0
	onPacketDropped := func() { onPacketDroppedCalled++ }
	b := NewBuffer(&testDepacketizer{}, 30, time.Second, WithPacketDroppedHandler(onPacketDropped))

	// ooo
	b.Push(testTailPacket(5, 31))

	require.Len(t, b.Pop(false), 0)

	b.Push(testPacket(3, 31))
	b.Push(testHeadPacket(6, 32))
	b.Push(testHeadPacket(1, 31))

	require.Len(t, b.Pop(false), 0)

	b.Push(testPacket(2, 31))
	b.Push(testPacket(4, 31))

	pkts := b.Pop(false)
	require.Len(t, pkts, 5)
	for i, pkt := range pkts {
		require.Equal(t, uint16(i+1), pkt.SequenceNumber)
	}

	// push and pop (not empty)
	b.Push(testTailPacket(7, 32))

	require.Len(t, b.Pop(false), 2)

	// push and pop (empty)
	b.Push(testHeadPacket(8, 33))
	b.Push(testTailPacket(9, 33))

	require.Len(t, b.Pop(false), 2)

	// sn jump
	ts := uint32(34)
	for i := uint16(5000); i < 5058; i += 2 {
		b.Push(testHeadPacket(i, ts))
		b.Push(testTailPacket(i+1, ts))
		ts++
		require.Len(t, b.Pop(false), 0)
	}

	b.Push(testHeadPacket(5058, ts))
	b.Push(testTailPacket(5059, ts))
	require.Len(t, b.Pop(false), 60)

	// sn wrap
	for i := uint16(65478); i > 65000; i += 2 {
		b.Push(testHeadPacket(i, ts))
		b.Push(testTailPacket(i+1, ts))
		ts++
	}
	require.Len(t, b.Pop(false), 0)

	b.Push(testHeadPacket(0, ts))
	b.Push(testTailPacket(1, ts))
	ts++
	require.Len(t, b.Pop(false), 60)
	require.Equal(t, 0, onPacketDroppedCalled)

	// dropped packets
	b.Push(testHeadPacket(2, ts))
	ts += 31
	b.Push(testHeadPacket(64, ts))
	b.Push(testTailPacket(65, ts))
	ts++

	require.Len(t, b.Pop(false), 0)
	// packet 2 dropped
	require.Equal(t, 1, onPacketDroppedCalled)

	// push packets 66-126
	for i := uint16(66); i < 122; i += 2 {
		b.Push(testHeadPacket(i, ts))
		b.Push(testTailPacket(i+1, ts))
		ts++

		// still waiting on packets 3-63
		require.Len(t, b.Pop(false), 0)
	}

	b.Push(testHeadPacket(122, ts))
	b.Push(testTailPacket(123, ts))

	require.Len(t, b.Pop(false), 60)
	// packets 3-63 lost
	require.Equal(t, 2, onPacketDroppedCalled)

	// ts wrap
	ts = 4294967280
	for i := uint16(15000); i < 15058; i += 2 {
		b.Push(testHeadPacket(i, ts))
		b.Push(testTailPacket(i+1, ts))
		ts++
		require.Len(t, b.Pop(false), 0)
	}
	b.Push(testHeadPacket(15058, ts))
	b.Push(testTailPacket(15059, ts))
	ts++
	require.Len(t, b.Pop(false), 60)

	// sn and ts jumps with drops
	b.Push(testTailPacket(15061, ts))
	b.Push(testHeadPacket(4000, 20000))
	b.Push(testTailPacket(4001, 20000))
	b.Push(testHeadPacket(15060, ts))
	b.Push(testHeadPacket(15062, ts+1))
	b.Push(testTailPacket(4003, 20001))

	require.Len(t, b.Pop(false), 2)

	ts = 20002
	for i := uint16(4004); i < 4062; i += 2 {
		b.Push(testHeadPacket(i, ts))
		b.Push(testTailPacket(i+1, ts))
		ts++
		require.Len(t, b.Pop(false), 0)
	}

	b.Push(testHeadPacket(4062, ts))
	b.Push(testTailPacket(4063, ts))
	ts++

	require.Len(t, b.Pop(false), 2)
	// packet 15062 dropped
	require.Equal(t, 3, onPacketDroppedCalled)

	b.Push(testHeadPacket(4064, ts))
	b.Push(testTailPacket(4065, ts))
	ts++

	// packet 4002 lost, 4003 dropped
	require.Len(t, b.Pop(false), 62)
	require.Equal(t, 4, onPacketDroppedCalled)

	// samples
	b.Push(testHeadPacket(4066, ts))
	b.Push(testTailPacket(4067, ts))
	ts++
	b.Push(testHeadPacket(4068, ts))
	b.Push(testTailPacket(4069, ts))
	ts++

	require.Len(t, b.PopSamples(false), 2)
}

type testDepacketizer struct{}

var headerBytes = []byte{0xaa, 0xaa}

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

const defaultPacketSize = 200

func testPacket(sn uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: sn,
			Timestamp:      ts,
		},
		Payload: make([]byte, defaultPacketSize),
	}
}

func testHeadPacket(sn uint16, ts uint32) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: sn,
			Timestamp:      ts,
		},
		Payload: make([]byte, defaultPacketSize),
	}
	copy(p.Payload, headerBytes)
	return p
}

func testTailPacket(sn uint16, ts uint32) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			Marker:         true,
			SequenceNumber: sn,
			Timestamp:      ts,
		},
		Payload: make([]byte, defaultPacketSize),
	}
	return p
}
