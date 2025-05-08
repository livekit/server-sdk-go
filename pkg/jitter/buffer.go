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
	"sync"
	"time"

	"github.com/pion/rtp"
)

type Buffer struct {
	depacketizer rtp.Depacketizer
	latency      time.Duration
	out          chan []*rtp.Packet
	onPacketLoss func()

	mu sync.Mutex

	initialized bool
	prevSN      uint16
	head        *packet
	tail        *packet

	stats *BufferStats
	timer *time.Timer

	pool *packet
	size int
}

type BufferStats struct {
	PacketsPushed  uint64 // total packets pushed
	PaddingPushed  uint64 // padding packets pushed
	PacketsLost    uint64 // packets lost
	PacketsDropped uint64 // packets dropped (incomplete)
	PacketsPopped  uint64 // packets sent to handler
	SamplesPopped  uint64 // samples sent to handler
}

func NewBuffer(
	depacketizer rtp.Depacketizer,
	latency time.Duration,
	out chan []*rtp.Packet,
	onPacketLoss func(),
) *Buffer {
	b := &Buffer{
		depacketizer: depacketizer,
		latency:      latency,
		out:          out,
		onPacketLoss: onPacketLoss,
		stats:        &BufferStats{},
		timer:        time.NewTimer(latency),
	}

	go func() {
		for range b.timer.C {
			b.mu.Lock()
			b.popReady()
			b.mu.Unlock()
		}
	}()

	return b
}

func (b *Buffer) UpdateLatency(latency time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.latency = latency
}

func (b *Buffer) Push(pkt *rtp.Packet) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.push(pkt)
	if b.head == nil {
		return
	}

	b.popReady()
}

func (b *Buffer) Size() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.size
}

func (b *Buffer) Stats() *BufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	return &BufferStats{
		PacketsPushed:  b.stats.PacketsPushed,
		PaddingPushed:  b.stats.PaddingPushed,
		PacketsLost:    b.stats.PacketsLost,
		PacketsDropped: b.stats.PacketsDropped,
		PacketsPopped:  b.stats.PacketsPopped,
		SamplesPopped:  b.stats.SamplesPopped,
	}
}

func (s *BufferStats) PacketLoss() float64 {
	if s.PacketsPushed == 0 {
		return 0
	}

	return float64(s.PacketsDropped) / float64(s.PacketsPushed)
}

func (b *Buffer) Close() {
	b.timer.Stop()
}

// push adds a packet to the buffer
func (b *Buffer) push(pkt *rtp.Packet) {
	b.stats.PacketsPushed++
	if pkt.Padding {
		b.stats.PaddingPushed++
		if !b.initialized {
			return
		}
	}

	if b.initialized && before(pkt.SequenceNumber, b.prevSN) {
		// packet expired
		if !pkt.Padding {
			b.stats.PacketsDropped++
			if b.onPacketLoss != nil {
				b.onPacketLoss()
			}
		}
		return
	}

	p := b.newPacket(pkt)

	discont := !b.initialized || !withinRange(pkt.SequenceNumber, b.prevSN)

	if b.head == nil {
		p.discont = discont && p.start
		b.head = p
		b.tail = p
		return
	}

	beforeHead := before(pkt.SequenceNumber, b.head.packet.SequenceNumber)
	afterTail := !before(pkt.SequenceNumber, b.tail.packet.SequenceNumber)
	withinHeadRange := withinRange(pkt.SequenceNumber, b.head.packet.SequenceNumber)
	withinTailRange := withinRange(pkt.SequenceNumber, b.tail.packet.SequenceNumber)

	switch {
	case beforeHead && withinHeadRange:
		// prepend
		p.discont = discont && p.start
		b.head.prev = p
		p.next = b.head
		b.head = p

	case afterTail && withinTailRange:
		// append
		p.prev = b.tail
		b.tail.next = p
		b.tail = p

	case withinTailRange:
		// insert, search from tail
		for c := b.tail.prev; c != nil; c = c.prev {
			discont = !withinRange(pkt.SequenceNumber, c.packet.SequenceNumber)
			if !before(pkt.SequenceNumber, c.packet.SequenceNumber) || discont {
				// insert after c
				p.discont = discont && p.start
				p.prev = c
				p.next = c.next
				c.next.prev = p
				c.next = p
				return
			}
		}

	case withinHeadRange:
		// insert, search from head
		for c := b.head.next; c != nil; c = c.next {
			discont = !withinRange(pkt.SequenceNumber, c.packet.SequenceNumber)
			if before(pkt.SequenceNumber, c.packet.SequenceNumber) || discont {
				// insert before c
				p.prev = c.prev
				p.next = c
				c.prev.next = p
				c.prev = p
				return
			}
		}

	default:
		// append (discont)
		p.discont = p.start
		p.prev = b.tail
		b.tail.next = p
		b.tail = p
	}
}

// popReady pushes all ready samples to the out channel
func (b *Buffer) popReady() {
	expiry := time.Now().Add(-b.latency)

	b.dropIncompleteExpired(expiry)

	loss := false
	for b.head != nil &&
		b.head.isComplete() {

		if b.head.packet.SequenceNumber == b.prevSN+1 || b.head.discont || !b.initialized {
			if sample := b.popSample(); len(sample) > 0 {
				b.out <- sample
			}
		} else if b.head.received.Before(expiry) {
			loss = true
			b.stats.PacketsLost += uint64(b.head.packet.SequenceNumber - b.prevSN - 1)
			if sample := b.popSample(); len(sample) > 0 {
				b.out <- sample
			}
		} else {
			break
		}
	}

	if loss && b.onPacketLoss != nil {
		b.onPacketLoss()
	}

	if b.head != nil {
		b.timer.Reset(time.Until(b.head.received.Add(b.latency)))
	}
}

// dropIncompleteExpired drops incomplete expired packets
func (b *Buffer) dropIncompleteExpired(expiry time.Time) {
	dropped := false

	for b.head != nil && !b.head.isComplete() && b.head.received.Before(expiry) {
		if b.initialized && !b.head.discont {
			b.stats.PacketsLost += uint64(b.head.packet.SequenceNumber - b.prevSN - 1)
		}

		b.free(b.popHead())

		dropped = true
		b.stats.PacketsDropped++
	}

	if dropped && b.onPacketLoss != nil {
		b.onPacketLoss()
	}
}

func (b *Buffer) popSample() []*rtp.Packet {
	sample := make([]*rtp.Packet, 0, b.size)
	end := false
	for !end {
		c := b.popHead()
		end = c.end

		if !c.packet.Padding {
			sample = append(sample, c.packet)
		}

		b.stats.PacketsPopped++
		b.free(c)
	}

	b.initialized = true
	b.stats.SamplesPopped++

	return sample
}

func (b *Buffer) popHead() *packet {
	c := b.head
	b.prevSN = c.packet.SequenceNumber
	b.head = c.next
	if b.head == nil {
		b.tail = nil
	} else {
		b.head.prev = nil
	}
	return c
}

func before(a, b uint16) bool {
	return (b-a)&0x8000 == 0
}

func withinRange(a, b uint16) bool {
	return a-b < 3000 || b-a < 3000
}
