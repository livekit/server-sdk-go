// Copyright 2026 LiveKit, Inc.
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

package datatrack

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPacketizer_FrameMarker(t *testing.T) {
	for _, tc := range []struct {
		index, packetCount int
		expected           FrameMarker
	}{
		{0, 1, FrameMarkerSingle},
		{0, 10, FrameMarkerStart},
		{4, 10, FrameMarkerInter},
		{9, 10, FrameMarkerFinal},
	} {
		t.Run(fmt.Sprintf("index=%d,count=%d", tc.index, tc.packetCount), func(t *testing.T) {
			require.Equal(t, tc.expected, frameMarker(tc.index, tc.packetCount))
		})
	}
}

func TestPacketizer_Packetize(t *testing.T) {
	for _, tc := range []struct {
		name         string
		payloadSize  int
		mtuSize      int
		packetsCount int
	}{
		{"zero_payload", 0, 1_024, 0},
		{"single_packet", 128, 1_024, 1},
		{"multi_packet", 20_480, 1_024, 21},
		{"multi_packet_mtu_16000", 40_960, 16_000, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handle := trackHandle(0x8811)
			extensions := testExtensions()

			p := newPacketizer(handle, tc.mtuSize)
			packets, err := p.packetize(bytes.Repeat([]byte{0xab}, tc.payloadSize), extensions)
			require.NoError(t, err)

			if len(packets) == 0 {
				require.Zero(t, tc.payloadSize, "should be no packets for zero payload")
				return
			}
			for i, packet := range packets {
				require.Equal(t, frameMarker(i, len(packets)), markerOf(&packet.Header))
				require.Equal(t, uint16(0), packet.FrameNumber)
				require.Equal(t, uint16(handle), packet.Handle)
				require.Equal(t, uint16(i), packet.SequenceNumber)
				require.Equal(t, extensions, extensionsOf(&packet.Header))
			}
		})
	}
}

func TestChunkPayload_EmptySource(t *testing.T) {
	require.Empty(t, chunkPayload(nil, 256))
}

func TestChunkPayload_Chunks(t *testing.T) {
	for _, chunkSize := range []int{1, 128, 333} {
		for _, sourceSize := range []int{1, 64, 128, 256, 123} {
			t.Run(fmt.Sprintf("chunk=%d,source=%d", chunkSize, sourceSize), func(t *testing.T) {
				chunks := chunkPayload(bytes.Repeat([]byte{0xcc}, sourceSize), chunkSize)

				require.Len(t, chunks, (sourceSize+chunkSize-1)/chunkSize)
				for _, chunk := range chunks[:len(chunks)-1] {
					require.Len(t, chunk, chunkSize)
				}
				expectedLastLen := sourceSize % chunkSize
				if expectedLastLen == 0 {
					expectedLastLen = min(chunkSize, sourceSize)
				}
				require.Len(t, chunks[len(chunks)-1], expectedLastLen)
			})
		}
	}
}
