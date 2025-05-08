// Copyright 2025 LiveKit, Inc.
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
	"time"

	"github.com/pion/rtp"
)

type packet struct {
	received   time.Time
	prev, next *packet
	start, end bool
	discont    bool
	packet     *rtp.Packet
}

func (b *Buffer) newPacket(pkt *rtp.Packet) *packet {
	b.size++

	p := b.pool
	if p == nil {
		p = &packet{}
	}
	b.pool = p.next

	p.received = time.Now()
	p.prev = nil
	p.next = nil
	p.start = b.depacketizer.IsPartitionHead(pkt.Payload)
	p.end = b.depacketizer.IsPartitionTail(pkt.Marker, pkt.Payload)
	p.packet = pkt

	return p
}

// isComplete checks if the packet is the start of a complete sample
func (p *packet) isComplete() bool {
	if !p.start {
		return false
	}
	if p.end {
		return true
	}

	for c := p; c.next != nil; c = c.next {
		if c.next.packet.SequenceNumber != c.packet.SequenceNumber+1 {
			return false
		}
		if c.next.end {
			return true
		}
	}

	return false
}

func (b *Buffer) free(pkt *packet) {
	b.size--

	pkt.prev = nil
	pkt.packet = nil
	pkt.next = b.pool

	b.pool = pkt
}
