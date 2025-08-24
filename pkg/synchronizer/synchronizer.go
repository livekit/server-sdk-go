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

const (
	DefaultMaxTsDiff = time.Minute
)

type SynchronizerOption func(*SynchronizerConfig)

// SynchronizerConfig holds configuration for the Synchronizer
type SynchronizerConfig struct {
	MaxTsDiff                  time.Duration
	OnStarted                  func()
	AudioPTSAdjustmentDisabled bool
}

// WithMaxTsDiff sets the maximum acceptable difference between RTP packets
// In case exceeded the synchronizer will adjust the PTS offset to keep the audio and video in sync
func WithMaxTsDiff(maxTsDiff time.Duration) SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.MaxTsDiff = maxTsDiff
	}
}

// WithOnStarted sets the callback to be called when the synchronizer starts
func WithOnStarted(onStarted func()) SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.OnStarted = onStarted
	}
}

// WithAudioPTSAdjustmentDisabled - disables auto PTS adjustments after sender reports
// Use case: when media processing pipeline needs stable - monotonically increasing
// PTS sequence - small adjustments coming from RTCP sender reports could cause gaps in the audio
// Media processing pipeline could opt out of auto PTS adjustments and handle the gap
// by e.g modifying tempo to compensate instead
func WithAudioPTSAdjustmentDisabled() SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.AudioPTSAdjustmentDisabled = true
	}
}

// a single Synchronizer is shared between all audio and video writers
type Synchronizer struct {
	sync.RWMutex

	startedAt int64
	onStarted func()
	endedAt   int64
	config    SynchronizerConfig

	psByIdentity map[string]*participantSynchronizer
	psBySSRC     map[uint32]*participantSynchronizer
	ssrcByID     map[string]uint32
}

func NewSynchronizer(onStarted func()) *Synchronizer {
	config := SynchronizerConfig{
		MaxTsDiff: DefaultMaxTsDiff,
		OnStarted: onStarted,
	}

	return &Synchronizer{
		onStarted:    config.OnStarted,
		psByIdentity: make(map[string]*participantSynchronizer),
		psBySSRC:     make(map[uint32]*participantSynchronizer),
		ssrcByID:     make(map[string]uint32),
		config:       config,
	}
}

func NewSynchronizerWithOptions(opts ...SynchronizerOption) *Synchronizer {
	config := SynchronizerConfig{
		MaxTsDiff: DefaultMaxTsDiff,
		OnStarted: nil,
	}

	for _, opt := range opts {
		opt(&config)
	}

	return &Synchronizer{
		onStarted:    config.OnStarted,
		psByIdentity: make(map[string]*participantSynchronizer),
		psBySSRC:     make(map[uint32]*participantSynchronizer),
		ssrcByID:     make(map[string]uint32),
		config:       config,
	}
}

func (s *Synchronizer) AddTrack(track TrackRemote, identity string) *TrackSynchronizer {
	t := newTrackSynchronizer(s, track)

	s.Lock()
	p := s.psByIdentity[identity]
	if p == nil {
		p = newParticipantSynchronizer()
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
