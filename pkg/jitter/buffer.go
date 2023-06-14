package jitter

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp"

	"github.com/livekit/protocol/logger"
)

type Buffer struct {
	depacketizer    rtp.Depacketizer
	maxLate         uint32
	clockRate       uint32
	onPacketDropped func()
	logger          logger.Logger

	mu          sync.Mutex
	pool        *packet
	size        int
	initialized bool
	prevSN      uint16
	head        *packet
	tail        *packet

	maxSampleSize uint32
	minTS         uint32
}

type packet struct {
	prev, next     *packet
	start, end     bool
	reset, padding bool
	packet         *rtp.Packet
}

func NewBuffer(depacketizer rtp.Depacketizer, clockRate uint32, maxLatency time.Duration, opts ...Option) *Buffer {
	b := &Buffer{
		depacketizer: depacketizer,
		maxLate:      uint32(float64(maxLatency) / float64(time.Second) * float64(clockRate)),
		clockRate:    clockRate,
		logger:       logger.LogRLogger(logr.Discard()),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (b *Buffer) UpdateMaxLatency(maxLatency time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	maxLate := uint32(float64(maxLatency) / float64(time.Second) * float64(b.clockRate))
	b.minTS += b.maxLate - maxLate
	b.maxLate = maxLate
}

func (b *Buffer) Push(pkt *rtp.Packet) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var start, end, padding bool
	if len(pkt.Payload) == 0 {
		start = true
		end = true
		padding = true
		// drop padding packets from the beginning of the stream
		if !b.initialized {
			return
		}
	} else {
		start = b.depacketizer.IsPartitionHead(pkt.Payload)
		end = b.depacketizer.IsPartitionTail(pkt.Marker, pkt.Payload)
	}

	p := b.newPacket(start, end, padding, pkt)

	beforePrev := before(pkt.SequenceNumber, b.prevSN)
	outsidePrevRange := outsideRange(pkt.SequenceNumber, b.prevSN)

	// drop if packet comes before previously pushed packet
	if b.initialized && beforePrev && !outsidePrevRange {
		b.logger.Debugw("packet dropped",
			"sequence number", pkt.SequenceNumber,
			"timestamp", pkt.Timestamp,
			"reason", fmt.Sprintf("already pushed %v", b.prevSN),
		)
		if b.onPacketDropped != nil {
			b.onPacketDropped()
		}
		return
	}

	if b.tail == nil {
		// list is empty
		if !b.initialized {
			b.initialized = true
			p.reset = p.start
		} else {
			p.reset = p.start && outsidePrevRange
		}

		b.minTS = pkt.Timestamp - b.maxLate
		b.head = p
		b.tail = p
		return
	}

	beforeHead := before(pkt.SequenceNumber, b.head.packet.SequenceNumber)
	beforeTail := before(pkt.SequenceNumber, b.tail.packet.SequenceNumber)
	outsideHeadRange := outsideRange(pkt.SequenceNumber, b.head.packet.SequenceNumber)
	outsideTailRange := outsideRange(pkt.SequenceNumber, b.tail.packet.SequenceNumber)

	switch {
	case !beforeTail && !outsideTailRange:
		// append (within range)
		b.minTS += pkt.Timestamp - b.tail.packet.Timestamp
		if pkt.SequenceNumber == b.tail.packet.SequenceNumber+1 {
			if f := pkt.Timestamp - b.tail.packet.Timestamp; f > b.maxSampleSize {
				b.maxSampleSize = f
			}
		}
		p.prev = b.tail
		b.tail.next = p
		b.tail = p

	case outsideHeadRange && outsideTailRange:
		// append (reset)
		p.reset = p.start
		b.minTS += b.maxSampleSize
		p.prev = b.tail
		b.tail.next = p
		b.tail = p

	case beforeHead && !outsideHeadRange:
		// prepend (within range)
		p.reset = p.start && outsidePrevRange
		b.head.prev = p
		p.next = b.head
		b.head = p

	case outsideTailRange:
		// insert (within head range)
		for c := b.tail.prev; c != nil; c = c.prev {
			if before(pkt.SequenceNumber, c.packet.SequenceNumber) || outsideRange(pkt.SequenceNumber, c.packet.SequenceNumber) {
				continue
			}

			// insert after c
			if pkt.SequenceNumber == c.packet.SequenceNumber+1 {
				if f := pkt.Timestamp - c.packet.Timestamp; f > b.maxSampleSize {
					b.maxSampleSize = f
				}
			}
			c.next.prev = p
			p.next = c.next
			p.prev = c
			c.next = p
			break
		}

	default:
		// insert (within tail range)
		for c := b.tail.prev; c != nil; c = c.prev {
			outsideCRange := outsideRange(pkt.SequenceNumber, c.packet.SequenceNumber)
			if before(pkt.SequenceNumber, c.packet.SequenceNumber) && !outsideCRange {
				continue
			}

			// insert after c
			if p.start && outsideCRange {
				p.reset = true
			} else if pkt.SequenceNumber == c.packet.SequenceNumber+1 {
				if f := pkt.Timestamp - c.packet.Timestamp; f > b.maxSampleSize {
					b.maxSampleSize = f
				}
			}
			c.next.prev = p
			p.next = c.next
			p.prev = c
			c.next = p
			break
		}
	}
}

func (b *Buffer) Pop(force bool) []*rtp.Packet {
	b.mu.Lock()
	defer b.mu.Unlock()

	if force {
		return b.forcePop()
	} else {
		return b.pop()
	}
}

func (b *Buffer) forcePop() []*rtp.Packet {
	packets := make([]*rtp.Packet, 0, b.size)
	var next *packet
	for c := b.head; c != nil; c = next {
		next = c.next
		packets = append(packets, c.packet)
		b.free(c)
	}
	b.head = nil
	b.tail = nil
	return packets
}

func (b *Buffer) pop() []*rtp.Packet {
	b.drop()

	if b.tail == nil {
		return nil
	}

	prevSN := b.prevSN
	prevComplete := true
	var end *packet
	for c := b.head; c != nil; c = c.next {
		// sequence number must be next, or a reset with a completed previous sample
		if c.packet.SequenceNumber != prevSN+1 && (!prevComplete || !c.reset) {
			break
		}
		if prevComplete {
			prevComplete = false
		}
		if c.end {
			end = c
			prevComplete = true
		}
		prevSN = c.packet.SequenceNumber
	}
	if end == nil {
		return nil
	}

	packets := make([]*rtp.Packet, 0, b.size)
	var next *packet
	for c := b.head; ; c = next {
		next = c.next
		if !c.padding {
			packets = append(packets, c.packet)
		}
		if next != nil {
			if outsideRange(next.packet.SequenceNumber, c.packet.SequenceNumber) {
				// adjust minTS to account for sequence number reset
				b.minTS += next.packet.Timestamp - c.packet.Timestamp - b.maxSampleSize
			}
			next.prev = nil
		}
		if c == end {
			b.prevSN = c.packet.SequenceNumber
			b.head = next
			if next == nil {
				b.tail = nil
			}
			b.free(c)
			return packets
		}
		b.free(c)
	}
}

func (b *Buffer) drop() {
	dropped := false
	for c := b.head; c != nil; {
		// check if timestamp is greater than minimum
		if (c.packet.Timestamp-b.minTS)&0x80000000 == 0 {
			break
		}

		// drop all packets within this sample
		dropped = true
		timestamp := c.packet.Timestamp
		for {
			b.logger.Debugw("packet dropped",
				"sequence number", c.packet.SequenceNumber,
				"timestamp", c.packet.Timestamp,
				"reason", fmt.Sprintf("minimum timestamp %v", b.minTS),
			)

			b.head = c.next
			if b.head == nil {
				// list has been emptied
				b.tail = nil
				b.prevSN = c.packet.SequenceNumber
				b.free(c)
				break
			}

			b.head.prev = nil
			if outsideRange(b.head.packet.SequenceNumber, c.packet.SequenceNumber) {
				// adjust minTS to account for sequence number reset
				b.minTS += b.head.packet.Timestamp - c.packet.Timestamp - b.maxSampleSize
			}

			b.prevSN = c.packet.SequenceNumber
			b.free(c)
			c = b.head

			// break if head is part of a new sample
			if b.head.packet.Timestamp != timestamp {
				break
			}
		}
	}

	if b.head.packet.SequenceNumber != b.prevSN+1 {
		if b.head.packet.Timestamp-b.maxSampleSize <= b.minTS {
			// lost packets will now be too old even if we receive them
			b.prevSN = b.head.packet.SequenceNumber - 1
		}
	}

	if dropped && b.onPacketDropped != nil {
		b.onPacketDropped()
	}
}

func (b *Buffer) newPacket(start, end, padding bool, pkt *rtp.Packet) *packet {
	b.size++
	if b.pool == nil {
		return &packet{
			start:   start,
			end:     end,
			padding: padding,
			packet:  pkt,
		}
	}

	p := b.pool
	b.pool = p.next
	p.next = nil
	p.start = start
	p.end = end
	p.padding = padding
	p.packet = pkt
	return p
}

func (b *Buffer) free(pkt *packet) {
	b.size--
	pkt.prev = nil
	pkt.packet = nil
	pkt.next = b.pool
	b.pool = pkt
}

func before(a, b uint16) bool {
	return (b-a)&0x8000 == 0
}

func outsideRange(a, b uint16) bool {
	return a-b > 3000 && b-a > 3000
}
