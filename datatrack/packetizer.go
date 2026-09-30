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
	"errors"

	dtp "github.com/livekit/protocol/datatrack"
)

var errMTUTooShort = errors.New("MTU is too short to send frame")

// packetizer converts frames into packets for transport.
type packetizer struct {
	handle      trackHandle
	mtu         int
	sequence    uint16
	frameNumber uint16
	clock       *clock
}

func newPacketizer(handle trackHandle, mtu int) *packetizer {
	return &packetizer{handle: handle, mtu: mtu, clock: newClock(randomTimestamp())}
}

func (p *packetizer) packetize(payload []byte, extensions Extensions) ([]dtp.Packet, error) {
	header := dtp.Header{
		Version:   supportedVersion,
		Handle:    uint16(p.handle),
		Timestamp: uint32(p.clock.now()),
	}
	extensions.apply(&header)

	maxPayloadSize := p.mtu - header.MarshalSize()
	if maxPayloadSize <= 0 {
		return nil, errMTUTooShort
	}
	header.FrameNumber = p.frameNumber
	p.frameNumber++

	chunks := chunkPayload(payload, maxPayloadSize)
	packets := make([]dtp.Packet, len(chunks))
	for i, chunk := range chunks {
		packetHeader := header
		frameMarker(i, len(chunks)).apply(&packetHeader)
		packetHeader.SequenceNumber = p.sequence
		p.sequence++
		packets[i] = dtp.Packet{Header: packetHeader, Payload: chunk}
	}
	return packets, nil
}

func frameMarker(index, packetCount int) FrameMarker {
	switch {
	case packetCount <= 1:
		return FrameMarkerSingle
	case index == 0:
		return FrameMarkerStart
	case index == packetCount-1:
		return FrameMarkerFinal
	default:
		return FrameMarkerInter
	}
}

// chunkPayload splits payload into consecutive chunks of at most maxSize bytes without copying.
func chunkPayload(payload []byte, maxSize int) [][]byte {
	var chunks [][]byte
	for len(payload) > 0 {
		n := min(maxSize, len(payload))
		chunks = append(chunks, payload[:n])
		payload = payload[n:]
	}
	return chunks
}
