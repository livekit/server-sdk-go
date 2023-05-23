package synchronizer

import (
	"sync"
	"time"

	"github.com/pion/rtcp"
)

// a single Synchronizer is shared between all audio and video writers
type Synchronizer struct {
	sync.RWMutex

	startedAt int64
	onStarted func()
	endedAt   int64

	psByIdentity map[string]*participantSynchronizer
	psBySSRC     map[uint32]*participantSynchronizer
}

func NewSynchronizer(onStarted func()) *Synchronizer {
	return &Synchronizer{
		onStarted:    onStarted,
		psByIdentity: make(map[string]*participantSynchronizer),
		psBySSRC:     make(map[uint32]*participantSynchronizer),
	}
}

func (s *Synchronizer) AddTrack(track TrackRemote, identity string) *TrackSynchronizer {
	t := newTrackSynchronizer(s, track)

	s.Lock()
	p := s.psByIdentity[identity]
	if p == nil {
		p = &participantSynchronizer{
			tracks:        make(map[uint32]*TrackSynchronizer),
			senderReports: make(map[uint32]*rtcp.SenderReport),
		}
		s.psByIdentity[identity] = p
	}
	ssrc := uint32(track.SSRC())
	s.psBySSRC[ssrc] = p
	s.Unlock()

	p.Lock()
	p.tracks[ssrc] = t
	p.Unlock()

	return t
}

func (s *Synchronizer) GetStartedAt() int64 {
	s.RLock()
	defer s.RUnlock()

	return s.startedAt
}

func (s *Synchronizer) getOrSetStartedAt(now int64) int64 {
	s.Lock()
	defer s.Unlock()

	if s.startedAt == 0 {
		s.startedAt = now
		if s.onStarted != nil {
			s.onStarted()
		}
	}

	return s.startedAt
}

// OnRTCP syncs a/v using sender reports
func (s *Synchronizer) OnRTCP(packet rtcp.Packet) {
	switch pkt := packet.(type) {
	case *rtcp.SenderReport:
		s.Lock()
		p := s.psBySSRC[pkt.SSRC]
		endedAt := s.endedAt
		s.Unlock()

		if endedAt != 0 {
			return
		}

		p.onSenderReport(pkt)
	}
}

func (s *Synchronizer) End() {
	endTime := time.Now()

	s.Lock()
	defer s.Unlock()

	// find the earliest time we can stop all tracks
	var maxOffset time.Duration
	for _, p := range s.psByIdentity {
		if m := p.getMaxOffset(); m > maxOffset {
			maxOffset = m
		}
	}
	s.endedAt = endTime.Add(maxOffset).UnixNano()
	maxPTS := time.Duration(s.endedAt - s.startedAt)

	// drain all
	for _, p := range s.psByIdentity {
		p.drain(maxPTS)
	}
}

func (s *Synchronizer) GetEndedAt() int64 {
	s.RLock()
	defer s.RUnlock()

	return s.endedAt
}
