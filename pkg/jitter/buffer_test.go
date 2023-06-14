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

	require.Nil(t, b.Pop(false))

	b.Push(testPacket(3, 31))
	b.Push(testHeadPacket(6, 32))
	b.Push(testHeadPacket(1, 31))

	require.Nil(t, b.Pop(false))

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

	// sn jump (empty)
	b.Push(testHeadPacket(4000, 34))
	b.Push(testHeadPacket(4002, 35))
	b.Push(testTailPacket(4001, 34))

	require.Len(t, b.Pop(false), 2)

	// sn jump (not empty)
	b.Push(testTailPacket(4003, 35))
	b.Push(testHeadPacket(8000, 36))
	b.Push(testTailPacket(8001, 36))

	require.Len(t, b.Pop(false), 4)

	// ooo sn jump (empty)
	b.Push(testTailPacket(13001, 37))
	b.Push(testHeadPacket(13000, 37))

	require.Len(t, b.Pop(false), 2)

	// ooo sn jump (not empty)
	b.Push(testHeadPacket(13002, 38))
	b.Push(testTailPacket(17001, 39))
	b.Push(testHeadPacket(17000, 39))

	require.Nil(t, b.Pop(false))

	b.Push(testTailPacket(13003, 38))

	require.Len(t, b.Pop(false), 4)

	// sn wrap
	b.Push(testHeadPacket(65533, 40))
	b.Push(testTailPacket(65534, 40))
	b.Push(testTailPacket(0, 41))
	b.Push(testHeadPacket(65535, 41))

	require.Len(t, b.Pop(false), 4)
	require.Equal(t, 0, onPacketDroppedCalled)

	// dropped packets
	b.Push(testHeadPacket(1, 42))
	ts := uint32(73)
	// push packets 60-89
	for i := uint16(60); i < 90; i += 2 {
		// waiting on packets 2-59
		require.Nil(t, b.Pop(false))

		b.Push(testHeadPacket(i, ts))
		b.Push(testTailPacket(i+1, ts))
		ts++
	}

	// packet 1 dropped, still waiting on packets 2-59
	require.Nil(t, b.Pop(false))
	require.Equal(t, 1, onPacketDroppedCalled)

	// push packets 90-119
	for i := uint16(90); i < 120; i += 2 {
		// still waiting on packets 2-59
		require.Nil(t, b.Pop(false))

		b.Push(testHeadPacket(i, ts))
		b.Push(testTailPacket(i+1, ts))
		ts++
	}

	// packets 2-59 would now be too old, consider them lost
	require.Len(t, b.Pop(false), 60)
	require.Equal(t, 2, onPacketDroppedCalled)

	// sn and ts jumps with drops
	b.Push(testTailPacket(121, 104))
	b.Push(testHeadPacket(4000, 20000))
	b.Push(testTailPacket(4001, 20000))

	require.Nil(t, b.Pop(false))

	b.Push(testHeadPacket(120, 104))

	require.Len(t, b.Pop(true), 4)

	b.Push(testHeadPacket(4002, 20001))
	b.Push(testHeadPacket(4004, 20002))
	b.Push(testTailPacket(4005, 20002))
	b.Push(testHeadPacket(8000, 1000))
	b.Push(testTailPacket(8001, 1001))
	b.Push(testHeadPacket(8002, 1030))
	b.Push(testTailPacket(8003, 1031))

	require.Len(t, b.Pop(false), 4)
	require.Equal(t, 3, onPacketDroppedCalled)

	// ts wrap
	b.Push(testHeadPacket(1000, 4294967295))
	b.Push(testTailPacket(1001, 4294967295))
	b.Push(testHeadPacket(1002, 0))
	b.Push(testTailPacket(1003, 0))

	require.Len(t, b.Pop(false), 4)
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
