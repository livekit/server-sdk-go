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
	ssrcByID     map[string]uint32
}

func NewSynchronizer(onStarted func()) *Synchronizer {
	return &Synchronizer{
		onStarted:    onStarted,
		psByIdentity: make(map[string]*participantSynchronizer),
		psBySSRC:     make(map[uint32]*participantSynchronizer),
		ssrcByID:     make(map[string]uint32),
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
	s.ssrcByID[track.ID()] = ssrc
	s.psBySSRC[ssrc] = p
	s.Unlock()

	p.Lock()
	p.tracks[ssrc] = t
	p.Unlock()

	return t
}

func (s *Synchronizer) RemoveTrack(trackID string) {
	s.Lock()
	ssrc := s.ssrcByID[trackID]
	p := s.psBySSRC[ssrc]
	delete(s.ssrcByID, trackID)
	delete(s.psBySSRC, ssrc)
	s.Unlock()
	if p == nil {
		return
	}

	p.Lock()
	if ts := p.tracks[ssrc]; ts != nil {
		ts.sync = nil
	}
	delete(p.tracks, ssrc)
	delete(p.senderReports, ssrc)
	p.Unlock()
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

		if endedAt != 0 || p == nil {
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
