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
	"fmt"
	"slices"

	dtp "github.com/livekit/protocol/datatrack"
)

const maxBufferPackets = 128

// depacketizer reassembles packets into frames.
type depacketizer struct {
	partials map[uint16]*partialFrame
	// frame numbers of partials in insertion order, oldest first
	order []uint16
}

type partialFrame struct {
	startSequence uint16
	extensions    Extensions
	payloads      map[uint16][]byte
}

type depacketizerFrame struct {
	payload    []byte
	extensions Extensions
}

type depacketizerPushOptions struct {
	// maxPartialFrames is the number of frames assembled concurrently; the oldest partial is
	// evicted when a new frame arrives at capacity.
	maxPartialFrames int
}

// depacketizerPushResult may carry both a completed frame and a dropped one, since a single
// packet can complete one frame while evicting another.
type depacketizerPushResult struct {
	frame *depacketizerFrame
	drop  *depacketizerDropError
}

type depacketizerDropReason int

const (
	dropReasonInterrupted depacketizerDropReason = iota
	dropReasonUnknownFrame
	dropReasonBufferFull
	dropReasonIncomplete
)

// depacketizerDropError records a dropped frame.
type depacketizerDropError struct {
	frameNumber uint16
	reason      depacketizerDropReason
	// newFrameNumber is the frame that interrupted the dropped one
	newFrameNumber uint16
	// received and expected packet counts of an incomplete frame
	received, expected uint16
}

func (e *depacketizerDropError) Error() string {
	var reason string
	switch e.reason {
	case dropReasonInterrupted:
		reason = fmt.Sprintf("interrupted by new frame %d", e.newFrameNumber)
	case dropReasonUnknownFrame:
		reason = "unknown frame"
	case dropReasonBufferFull:
		reason = "buffer full"
	case dropReasonIncomplete:
		reason = fmt.Sprintf("incomplete (%d/%d)", e.received, e.expected)
	}
	return fmt.Sprintf("frame %d dropped: %s", e.frameNumber, reason)
}

func newDepacketizer() *depacketizer {
	return &depacketizer{partials: make(map[uint16]*partialFrame)}
}

func (d *depacketizer) push(packet dtp.Packet, options depacketizerPushOptions) depacketizerPushResult {
	switch markerOf(&packet.Header) {
	case FrameMarkerSingle:
		return d.frameFromSingle(packet, options)
	case FrameMarkerStart:
		return d.beginPartial(packet, options)
	default:
		return d.pushToPartial(packet)
	}
}

func (d *depacketizer) frameFromSingle(packet dtp.Packet, options depacketizerPushOptions) depacketizerPushResult {
	var result depacketizerPushResult
	if len(d.partials) >= options.maxPartialFrames {
		result.drop = d.evictOldest(packet.FrameNumber)
	}
	result.frame = &depacketizerFrame{payload: packet.Payload, extensions: extensionsOf(&packet.Header)}
	return result
}

func (d *depacketizer) evictOldest(newFrameNumber uint16) *depacketizerDropError {
	if len(d.order) == 0 {
		return nil
	}
	oldest := d.order[0]
	d.remove(oldest)
	return &depacketizerDropError{frameNumber: oldest, reason: dropReasonInterrupted, newFrameNumber: newFrameNumber}
}

func (d *depacketizer) remove(frameNumber uint16) {
	delete(d.partials, frameNumber)
	if i := slices.Index(d.order, frameNumber); i >= 0 {
		d.order = slices.Delete(d.order, i, i+1)
	}
}

func (d *depacketizer) beginPartial(packet dtp.Packet, options depacketizerPushOptions) depacketizerPushResult {
	var result depacketizerPushResult
	for len(d.partials) >= options.maxPartialFrames {
		evicted := d.evictOldest(packet.FrameNumber)
		if evicted == nil {
			break
		}
		if result.drop == nil {
			result.drop = evicted
		}
	}

	d.partials[packet.FrameNumber] = &partialFrame{
		startSequence: packet.SequenceNumber,
		extensions:    extensionsOf(&packet.Header),
		payloads:      map[uint16][]byte{packet.SequenceNumber: packet.Payload},
	}
	d.order = append(d.order, packet.FrameNumber)
	return result
}

func (d *depacketizer) pushToPartial(packet dtp.Packet) depacketizerPushResult {
	frameNumber := packet.FrameNumber
	partial, ok := d.partials[frameNumber]
	if !ok {
		return depacketizerPushResult{drop: &depacketizerDropError{frameNumber: frameNumber, reason: dropReasonUnknownFrame}}
	}

	if len(partial.payloads) >= maxBufferPackets {
		d.remove(frameNumber)
		return depacketizerPushResult{drop: &depacketizerDropError{frameNumber: frameNumber, reason: dropReasonBufferFull}}
	}

	partial.payloads[packet.SequenceNumber] = packet.Payload

	if markerOf(&packet.Header) == FrameMarkerFinal {
		d.remove(frameNumber)
		return finalize(frameNumber, partial, packet.SequenceNumber)
	}
	return depacketizerPushResult{}
}

func finalize(frameNumber uint16, partial *partialFrame, endSequence uint16) depacketizerPushResult {
	received := uint16(len(partial.payloads))

	payloadLen := 0
	for _, payload := range partial.payloads {
		payloadLen += len(payload)
	}
	payload := make([]byte, 0, payloadLen)

	for sequence := partial.startSequence; ; sequence++ {
		partialPayload, ok := partial.payloads[sequence]
		if !ok {
			break
		}
		payload = append(payload, partialPayload...)
		if sequence == endSequence {
			return depacketizerPushResult{frame: &depacketizerFrame{payload: payload, extensions: partial.extensions}}
		}
	}
	return depacketizerPushResult{drop: &depacketizerDropError{
		frameNumber: frameNumber,
		reason:      dropReasonIncomplete,
		received:    received,
		expected:    endSequence - partial.startSequence + 1,
	}}
}
