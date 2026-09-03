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

	dtp "github.com/livekit/protocol/datatrack"
	"github.com/stretchr/testify/require"
)

var defaultPushOptions = depacketizerPushOptions{maxPartialFrames: 1}

// derive returns a copy of base with the given marker, sequence and frame number.
func derive(base dtp.Packet, marker FrameMarker, sequence, frameNumber uint16) dtp.Packet {
	packet := base
	marker.apply(&packet.Header)
	packet.SequenceNumber = sequence
	packet.FrameNumber = frameNumber
	return packet
}

func withPayload(packet dtp.Packet, payload []byte) dtp.Packet {
	packet.Payload = payload
	return packet
}

func TestDepacketizer_SinglePacket(t *testing.T) {
	d := newDepacketizer()

	packet := testPacket()
	FrameMarkerSingle.apply(&packet.Header)

	result := d.push(packet, defaultPushOptions)
	require.Nil(t, result.drop)
	require.NotNil(t, result.frame)
	require.Equal(t, packet.Payload, result.frame.payload)
	require.Equal(t, extensionsOf(&packet.Header), result.frame.extensions)
}

func TestDepacketizer_MultiPacket(t *testing.T) {
	for _, tc := range []struct {
		name         string
		interPackets int
	}{
		{"0", 0},
		{"8", 8},
		{"buffer_limit", maxBufferPackets - 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDepacketizer()

			packet := testPacket()
			FrameMarkerStart.apply(&packet.Header)

			result := d.push(packet, defaultPushOptions)
			require.True(t, result.frame == nil && result.drop == nil)

			for range tc.interPackets {
				FrameMarkerInter.apply(&packet.Header)
				packet.SequenceNumber++

				result = d.push(packet, defaultPushOptions)
				require.True(t, result.frame == nil && result.drop == nil)
			}

			FrameMarkerFinal.apply(&packet.Header)
			packet.SequenceNumber++

			result = d.push(packet, defaultPushOptions)
			require.Nil(t, result.drop)
			require.NotNil(t, result.frame)
			require.Equal(t, extensionsOf(&packet.Header), result.frame.extensions)
			require.Len(t, result.frame.payload, len(packet.Payload)*(tc.interPackets+2))
		})
	}
}

func TestDepacketizer_Interrupted(t *testing.T) {
	d := newDepacketizer()

	packet := testPacket()
	FrameMarkerStart.apply(&packet.Header)

	result := d.push(packet, defaultPushOptions)
	require.True(t, result.frame == nil && result.drop == nil)

	firstFrameNumber := packet.FrameNumber
	newFrameNumber := packet.FrameNumber + 1
	packet.FrameNumber = newFrameNumber

	result = d.push(packet, defaultPushOptions)
	require.Nil(t, result.frame)
	require.Equal(t, &depacketizerDropError{
		frameNumber:    firstFrameNumber,
		reason:         dropReasonInterrupted,
		newFrameNumber: newFrameNumber,
	}, result.drop)
}

func TestDepacketizer_Incomplete(t *testing.T) {
	d := newDepacketizer()

	packet := testPacket()
	frameNumber := packet.FrameNumber
	FrameMarkerStart.apply(&packet.Header)

	d.push(packet, defaultPushOptions)

	packet.SequenceNumber += 3
	FrameMarkerFinal.apply(&packet.Header)

	result := d.push(packet, defaultPushOptions)
	require.Nil(t, result.frame)
	require.Equal(t, &depacketizerDropError{
		frameNumber: frameNumber,
		reason:      dropReasonIncomplete,
		received:    2,
		expected:    4,
	}, result.drop)
}

func TestDepacketizer_UnknownFrame(t *testing.T) {
	d := newDepacketizer()

	packet := testPacket()
	frameNumber := packet.FrameNumber
	FrameMarkerInter.apply(&packet.Header)

	result := d.push(packet, defaultPushOptions)
	require.Equal(t, &depacketizerDropError{frameNumber: frameNumber, reason: dropReasonUnknownFrame}, result.drop)
}

func TestDepacketizer_MultiFrame(t *testing.T) {
	d := newDepacketizer()

	var sequence uint16
	next := func() uint16 {
		current := sequence
		sequence++
		return current
	}
	for frameNumber := range uint16(10) {
		packet := testPacket()
		packet.FrameNumber = frameNumber

		FrameMarkerStart.apply(&packet.Header)
		packet.SequenceNumber = next()
		result := d.push(packet, defaultPushOptions)
		require.True(t, result.drop == nil && result.frame == nil)

		FrameMarkerInter.apply(&packet.Header)
		packet.SequenceNumber = next()
		result = d.push(packet, defaultPushOptions)
		require.True(t, result.drop == nil && result.frame == nil)

		FrameMarkerFinal.apply(&packet.Header)
		packet.SequenceNumber = next()
		result = d.push(packet, defaultPushOptions)
		require.True(t, result.drop == nil && result.frame != nil)
	}
}

func TestDepacketizer_DuplicateSequenceNumbers(t *testing.T) {
	d := newDepacketizer()

	packet := testPacket()
	FrameMarkerStart.apply(&packet.Header)
	packet.SequenceNumber = 1
	packet.Payload = bytes.Repeat([]byte{0xab}, 3)

	result := d.push(packet, defaultPushOptions)
	require.True(t, result.drop == nil && result.frame == nil)

	FrameMarkerInter.apply(&packet.Header)
	packet.SequenceNumber = 1 // same sequence number
	packet.Payload = bytes.Repeat([]byte{0xcd}, 3)

	result = d.push(packet, defaultPushOptions)
	require.True(t, result.drop == nil && result.frame == nil)

	FrameMarkerFinal.apply(&packet.Header)
	packet.SequenceNumber = 2
	packet.Payload = bytes.Repeat([]byte{0xef}, 3)

	result = d.push(packet, defaultPushOptions)
	require.Nil(t, result.drop)
	require.NotNil(t, result.frame)
	require.True(t, bytes.HasPrefix(result.frame.payload, bytes.Repeat([]byte{0xcd}, 3)))
}

func TestDepacketizer_AssemblesMultiplePartialFrames(t *testing.T) {
	d := newDepacketizer()
	opts := depacketizerPushOptions{maxPartialFrames: 2}

	base := testPacket()
	payloadLen := len(base.Payload)

	result := d.push(derive(base, FrameMarkerStart, 0, 1), opts)
	require.True(t, result.frame == nil && result.drop == nil)

	result = d.push(derive(base, FrameMarkerStart, 100, 2), opts)
	require.True(t, result.frame == nil && result.drop == nil)

	result = d.push(derive(base, FrameMarkerFinal, 1, 1), opts)
	require.Nil(t, result.drop)
	require.NotNil(t, result.frame)
	require.Len(t, result.frame.payload, payloadLen*2)

	result = d.push(derive(base, FrameMarkerFinal, 101, 2), opts)
	require.Nil(t, result.drop)
	require.NotNil(t, result.frame)
	require.Len(t, result.frame.payload, payloadLen*2)
}

func TestDepacketizer_StartingNewFrameAtCapacity(t *testing.T) {
	d := newDepacketizer()
	opts := depacketizerPushOptions{maxPartialFrames: 2}

	base := testPacket()

	result := d.push(derive(base, FrameMarkerStart, 0, 1), opts)
	require.True(t, result.frame == nil && result.drop == nil)

	result = d.push(derive(base, FrameMarkerStart, 100, 2), opts)
	require.True(t, result.frame == nil && result.drop == nil)

	result = d.push(derive(base, FrameMarkerStart, 200, 3), opts)
	require.Nil(t, result.frame)
	require.Equal(t, &depacketizerDropError{frameNumber: 1, reason: dropReasonInterrupted, newFrameNumber: 3}, result.drop)
}

func TestDepacketizer_SinglePacketAtCapacity(t *testing.T) {
	d := newDepacketizer()
	opts := depacketizerPushOptions{maxPartialFrames: 2}

	base := testPacket()

	result := d.push(derive(base, FrameMarkerStart, 0, 1), opts)
	require.True(t, result.frame == nil && result.drop == nil)

	result = d.push(derive(base, FrameMarkerStart, 100, 2), opts)
	require.True(t, result.frame == nil && result.drop == nil)

	result = d.push(derive(base, FrameMarkerSingle, 200, 3), opts)
	require.Equal(t, &depacketizerDropError{frameNumber: 1, reason: dropReasonInterrupted, newFrameNumber: 3}, result.drop)
}

func TestDepacketizer_EvictsOldestWhenStartsExceedMax(t *testing.T) {
	d := newDepacketizer()
	opts := depacketizerPushOptions{maxPartialFrames: 5}

	const totalFrames uint16 = 10
	base := testPacket()

	for i := range totalFrames {
		require.Nil(t, d.push(derive(base, FrameMarkerStart, i*2, i+1), opts).frame)
	}

	producedFrames, unknownFrameErrors := 0, 0
	for i := range totalFrames {
		result := d.push(derive(base, FrameMarkerFinal, i*2+1, i+1), opts)
		if result.frame != nil {
			producedFrames++
		}
		if result.drop != nil {
			require.Equal(t, dropReasonUnknownFrame, result.drop.reason)
			unknownFrameErrors++
		}
	}

	require.Equal(t, 5, producedFrames)
	require.Equal(t, 5, unknownFrameErrors)
}

func TestDepacketizer_LatePacketsForEvictedFrame(t *testing.T) {
	d := newDepacketizer()
	opts := depacketizerPushOptions{maxPartialFrames: 3}

	base := testPacket()

	for i := uint16(1); i <= 3; i++ {
		require.Nil(t, d.push(derive(base, FrameMarkerStart, i*100, i), opts).frame)
	}

	// a fourth start evicts the oldest (frame 1)
	require.Nil(t, d.push(derive(base, FrameMarkerStart, 400, 4), opts).frame)

	result := d.push(derive(base, FrameMarkerInter, 101, 1), opts)
	require.Nil(t, result.frame)
	require.Equal(t, &depacketizerDropError{frameNumber: 1, reason: dropReasonUnknownFrame}, result.drop)

	result = d.push(derive(base, FrameMarkerFinal, 102, 1), opts)
	require.Nil(t, result.frame)
	require.Equal(t, &depacketizerDropError{frameNumber: 1, reason: dropReasonUnknownFrame}, result.drop)

	for _, frameNumber := range []uint16{2, 3, 4} {
		require.NotNil(t, d.push(derive(base, FrameMarkerFinal, frameNumber*100+1, frameNumber), opts).frame)
	}
}

func TestDepacketizer_HeavilyInterleavedFrames(t *testing.T) {
	d := newDepacketizer()
	opts := depacketizerPushOptions{maxPartialFrames: 3}

	base := testPacket()

	type frameSpec struct {
		frameNumber   uint16
		startSequence uint16
		payloads      [3][]byte
	}
	frames := []frameSpec{
		{1, 0, [3][]byte{{0xa1}, {0xa2}, {0xa3}}},
		{2, 100, [3][]byte{{0xb1}, {0xb2}, {0xb3}}},
		{3, 200, [3][]byte{{0xc1}, {0xc2}, {0xc3}}},
	}
	build := func(frameIdx int, packetIdx uint16, marker FrameMarker) dtp.Packet {
		f := frames[frameIdx]
		return withPayload(derive(base, marker, f.startSequence+packetIdx, f.frameNumber), f.payloads[packetIdx])
	}

	require.Nil(t, d.push(build(0, 0, FrameMarkerStart), opts).frame)
	require.Nil(t, d.push(build(1, 0, FrameMarkerStart), opts).frame)
	require.Nil(t, d.push(build(2, 0, FrameMarkerStart), opts).frame)
	require.Nil(t, d.push(build(0, 1, FrameMarkerInter), opts).frame)
	require.Nil(t, d.push(build(1, 1, FrameMarkerInter), opts).frame)
	require.Nil(t, d.push(build(2, 1, FrameMarkerInter), opts).frame)

	frameTwo := d.push(build(1, 2, FrameMarkerFinal), opts).frame
	require.NotNil(t, frameTwo)
	require.Equal(t, []byte{0xb1, 0xb2, 0xb3}, frameTwo.payload)

	frameOne := d.push(build(0, 2, FrameMarkerFinal), opts).frame
	require.NotNil(t, frameOne)
	require.Equal(t, []byte{0xa1, 0xa2, 0xa3}, frameOne.payload)

	frameThree := d.push(build(2, 2, FrameMarkerFinal), opts).frame
	require.NotNil(t, frameThree)
	require.Equal(t, []byte{0xc1, 0xc2, 0xc3}, frameThree.payload)
}

func TestDepacketizer_MaxPartialFramesChangeAcrossPushes(t *testing.T) {
	d := newDepacketizer()
	opts := depacketizerPushOptions{maxPartialFrames: 2}

	base := testPacket()
	startFor := func(frameNumber uint16) dtp.Packet {
		return derive(base, FrameMarkerStart, frameNumber*100, frameNumber)
	}
	finalFor := func(frameNumber uint16) dtp.Packet {
		return derive(base, FrameMarkerFinal, frameNumber*100+1, frameNumber)
	}

	require.Nil(t, d.push(startFor(1), opts).frame)
	require.Nil(t, d.push(startFor(2), opts).frame)

	// expanding the cap admits frames 3 and 4 without evicting anything
	opts.maxPartialFrames = 4

	result := d.push(startFor(3), opts)
	require.Nil(t, result.frame)
	require.Nil(t, result.drop)

	result = d.push(startFor(4), opts)
	require.Nil(t, result.frame)
	require.Nil(t, result.drop)

	require.NotNil(t, d.push(finalFor(1), opts).frame)

	// shrinking the cap evicts frames 2 and 3 when frame 5 starts; only the first eviction is reported
	opts.maxPartialFrames = 2

	require.Nil(t, d.push(startFor(5), opts).frame)

	result = d.push(finalFor(2), opts)
	require.Equal(t, &depacketizerDropError{frameNumber: 2, reason: dropReasonUnknownFrame}, result.drop)

	result = d.push(finalFor(3), opts)
	require.Equal(t, &depacketizerDropError{frameNumber: 3, reason: dropReasonUnknownFrame}, result.drop)

	require.NotNil(t, d.push(finalFor(4), opts).frame)
	require.NotNil(t, d.push(finalFor(5), opts).frame)
}

func TestDepacketizerDropError_Error(t *testing.T) {
	require.Equal(t, "frame 7 dropped: incomplete (2/4)", fmt.Sprint(&depacketizerDropError{
		frameNumber: 7, reason: dropReasonIncomplete, received: 2, expected: 4,
	}))
}
