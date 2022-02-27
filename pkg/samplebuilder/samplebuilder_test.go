package samplebuilder

import (
	"testing"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

type testDepacketizer struct {
	headBytes []byte
}

func (d *testDepacketizer) Unmarshal(r []byte) ([]byte, error) {
	return r, nil
}

func (d *testDepacketizer) IsPartitionHead(payload []byte) bool {
	if d.headBytes == nil || len(payload) < len(d.headBytes) {
		return false
	}
	for i, b := range d.headBytes {
		if payload[i] != b {
			return false
		}
	}
	return true
}

func (d *testDepacketizer) IsPartitionTail(marker bool, payload []byte) bool {
	return marker
}

func TestSampleBuilder(t *testing.T) {
	t.Run("out of order packets", func(t *testing.T) {
		sb := New(10, &testDepacketizer{}, 30)
		sb.Push(testPacketWithSN(5))
		require.Nil(t, sb.Pop())
		sb.Push(testPacketWithSN(3))
		sb.Push(testPacketWithSN(1))
		require.Nil(t, sb.Pop())
		require.NoError(t, sb.check())
		sb.Push(testPacketWithSN(2))
		sb.Push(testPacketWithSN(4))
		require.NoError(t, sb.check())
		for i, pkt := range sb.PopPackets() {
			require.Equal(t, i+1, pkt.SequenceNumber)
		}
	})

	t.Run("does not pop missing packets", func(t *testing.T) {
		sb := New(10, &testDepacketizer{}, 30)
		sb.Push(testHeadPacketWithSN(1))
		sb.Push(testPacketWithSN(5))
		require.Nil(t, sb.Pop())
	})

	t.Run("assembles samples", func(t *testing.T) {
		sb := New(10, &testDepacketizer{
			headBytes: headerBytes,
		}, 30)

		// first sample
		sb.Push(testPacketWithSN(2))
		sb.Push(testHeadPacketWithSN(1))
		sb.Push(testTailPacketWithSN(3))

		// second sample
		sb.Push(testHeadPacketWithSN(4))
		sb.Push(testTailPacketWithSN(5))
		sb.Push(testHeadPacketWithSN(6))
		require.NoError(t, sb.check())

		sample := sb.Pop()
		require.NotNil(t, sample)
		require.Equal(t, defaultPacketSize*3, len(sample.Data))
		require.Equal(t, headerBytes[0], sample.Data[0])

		sample2 := sb.Pop()
		require.NotNil(t, sample2)
		require.Equal(t, defaultPacketSize*2, len(sample2.Data))
	})
}

const defaultPacketSize = 200

var headerBytes = []byte{0xaa, 0xaa}

func testPacketWithSN(sn uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: sn,
		},
		Payload: make([]byte, defaultPacketSize),
	}
}

func testHeadPacketWithSN(sn uint16) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: sn,
		},
		Payload: make([]byte, defaultPacketSize),
	}
	copy(p.Payload, headerBytes)
	return p
}

func testTailPacketWithSN(sn uint16) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			Marker:         true,
			SequenceNumber: sn,
		},
		Payload: make([]byte, defaultPacketSize),
	}
	return p
}
