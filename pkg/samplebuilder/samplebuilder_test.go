package samplebuilder

import (
	"fmt"
	"testing"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

type testDepacketizer struct {
	headBytes []byte
	tailBytes []byte
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
	if marker {
		return true
	}
	if d.tailBytes == nil || len(payload) < len(d.tailBytes) {
		return false
	}
	start := len(payload) - len(d.tailBytes)
	for i, b := range d.tailBytes {
		if payload[i+start] != b {
			return false
		}
	}
	return true
}

func TestSampleBuilder(t *testing.T) {
	//t.Run("out of order packets", func(t *testing.T) {
	//	sb := New(10, &testDepacketizer{}, 30)
	//	sb.Push(testPacketWithSN(5, defaultPacketSize))
	//	sb.Push(testPacketWithSN(3, defaultPacketSize))
	//	sb.Push(testPacketWithSN(1, defaultPacketSize))
	//	require.NoError(t, sb.check())
	//	sb.Push(testPacketWithSN(2, defaultPacketSize))
	//	sb.Push(testPacketWithSN(4, defaultPacketSize))
	//	require.NoError(t, sb.check())
	//	for i, pkt := range sb.PopPackets() {
	//		require.Equal(t, i+1, pkt.SequenceNumber)
	//	}
	//})

	t.Run("assembles samples", func(t *testing.T) {
		sb := New(10, &testDepacketizer{
			headBytes: headerBytes,
			tailBytes: tailBytes,
		}, 30)
		// Pion's sample builder works, ours doesn't
		//sb := samplebuilder.New(10, &testDepacketizer{
		//	headBytes: headerBytes,
		//	tailBytes: tailBytes,
		//}, 30)
		sb.Push(testPacketWithSN(2, defaultPacketSize))
		head := testPacketWithSN(1, defaultPacketSize)
		copy(head.Payload, headerBytes)
		sb.Push(head)
		tail := testPacketWithSN(3, defaultPacketSize)
		copy(tail.Payload[defaultPacketSize-len(tailBytes):], tailBytes)
		sb.Push(tail)
		head2 := testPacketWithSN(4, defaultPacketSize)
		copy(head2.Payload, headerBytes)
		sb.Push(head2)
		tail2 := testPacketWithSN(5, defaultPacketSize)
		tail2.Marker = true
		sb.Push(tail2)
		sb.Push(testPacketWithSN(6, defaultPacketSize))
		//require.NoError(t, sb.check())

		fmt.Println("popping first sample")
		sample := sb.Pop()
		require.NotNil(t, sample)
		require.Equal(t, defaultPacketSize*3, len(sample.Data))
		require.Equal(t, headerBytes[0], sample.Data[0])

		fmt.Println("popping first sample")
		sample2 := sb.Pop()
		require.NotNil(t, sample2)
		require.Equal(t, defaultPacketSize*2, len(sample2.Data))
	})
}

const defaultPacketSize = 200

var headerBytes = []byte{0xaa, 0xaa}
var tailBytes = []byte{0xff, 0xff}

func testPacketWithSN(sn uint16, size int) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: sn,
		},
		Payload: make([]byte, size),
	}
}
