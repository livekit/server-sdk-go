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

	"github.com/livekit/protocol/utils/mono"
	"github.com/pion/rtcp"
)

const (
	DefaultMaxTsDiff                    = time.Minute
	DefaultMaxDriftAdjustment           = 5 * time.Millisecond
	DefaultDriftAdjustmentWindowPercent = 0.0 // 0 -> disables throttling
	DefaultOldPacketThreshold           = 500 * time.Millisecond
)

type SynchronizerOption func(*SynchronizerConfig)

// SynchronizerConfig holds configuration for the Synchronizer
type SynchronizerConfig struct {
	MaxTsDiff                         time.Duration
	MaxDriftAdjustment                time.Duration
	DriftAdjustmentWindowPercent      float64
	AudioPTSAdjustmentDisabled        bool
	PreJitterBufferReceiveTimeEnabled bool
	RTCPSenderReportRebaseEnabled     bool
	OldPacketThreshold                time.Duration
	EnableStartGate                   bool

	OnStarted func()

	MediaRunningTime         func() (time.Duration, bool)
	MaxMediaRunningTimeDelay time.Duration
}

// WithMaxTsDiff sets the maximum acceptable difference between RTP packets
// In case exceeded the synchronizer will adjust the PTS offset to keep the audio and video in sync
func WithMaxTsDiff(maxTsDiff time.Duration) SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.MaxTsDiff = maxTsDiff
	}
}

// WithMaxDriftAdjustment sets the maximum drift adjustment applied at a time
func WithMaxDriftAdjustment(maxDriftAdjustment time.Duration) SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.MaxDriftAdjustment = maxDriftAdjustment
	}
}

// WithDriftAdjustmentWinddowPercent controls throttling of how often drift adjustments are applied
// Throttles PTS adjustment to a limited amount in a time window.
// This setting determines how long a certain amount of adjustment
// throttles the next adjustment.
//
// For example, if a 1ms adjustment is appied at 1%, it means that
// 1ms is 1% of ajustment window, so the adjustment window is 100ms
// and next adjustment will not be applied till that time elapses
//
// With the settings of 5ms adjustment at 5%, a mamximum adjustment
// of 5ms per 100ms
func WithDriftAdjustmentWindowPercent(driftAdjustmentWindowPercent float64) SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.DriftAdjustmentWindowPercent = driftAdjustmentWindowPercent
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

// WithPreJitterBufferReceiveTimeEnabled - enables use of packet arrival time before it is
// added to the jitter buffer
func WithPreJitterBufferReceiveTimeEnabled() SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.PreJitterBufferReceiveTimeEnabled = true
	}
}

// WithRTCPSenderReportRebaseEnabled - enables rebasing RTCP Sender Report to local clock
func WithRTCPSenderReportRebaseEnabled() SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.RTCPSenderReportRebaseEnabled = true
	}
}

// WithOldPacketThreshold sets the threshold at which a packet is considered old
func WithOldPacketThreshold(oldPacketThreshold time.Duration) SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.OldPacketThreshold = oldPacketThreshold
	}
}

// WithStartGate enabled will buffer incoming packets until pacing stabilizes before initializing tracks
func WithStartGate() SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.EnableStartGate = true
	}
}

// WithOnStarted sets the callback to be called when the synchronizer starts
func WithOnStarted(onStarted func()) SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.OnStarted = onStarted
	}
}

// WithMediaRunningTime sets the callback to be called to get the media running time
// maxMediaRunningTimeDelay is the maximum allowed latency a packet can be behind the media running time
func WithMediaRunningTime(mediaRunningTime func() (time.Duration, bool), maxMediaRunningTimeDelay time.Duration) SynchronizerOption {
	return func(config *SynchronizerConfig) {
		config.MediaRunningTime = mediaRunningTime
		config.MaxMediaRunningTimeDelay = maxMediaRunningTimeDelay
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

	// start time of the external live media (if used, 0 otherwise)
	externalMediaStartTime time.Time
}

func NewSynchronizer(onStarted func()) *Synchronizer {
	config := SynchronizerConfig{
		MaxTsDiff:                    DefaultMaxTsDiff,
		MaxDriftAdjustment:           DefaultMaxDriftAdjustment,
		DriftAdjustmentWindowPercent: DefaultDriftAdjustmentWindowPercent,
		OldPacketThreshold:           DefaultOldPacketThreshold,
		OnStarted:                    onStarted,
		MediaRunningTime:             nil,
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
		MaxTsDiff:                    DefaultMaxTsDiff,
		MaxDriftAdjustment:           DefaultMaxDriftAdjustment,
		DriftAdjustmentWindowPercent: DefaultDriftAdjustmentWindowPercent,
		OldPacketThreshold:           DefaultOldPacketThreshold,
		OnStarted:                    nil,
		MediaRunningTime:             nil,
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
		ts.Close()
	}
	delete(p.tracks, ssrc)
	p.Unlock()
}

func (s *Synchronizer) GetStartedAt() int64 {
	s.RLock()
	defer s.RUnlock()

	return s.startedAt
}

// SetMediaRunningTime updates the external media running time provider after the synchronizer has been created.
// Passing a nil provider clears the configuration.
func (s *Synchronizer) SetMediaRunningTime(mediaRunningTime func() (time.Duration, bool)) {
	s.Lock()
	s.config.MediaRunningTime = mediaRunningTime
	s.Unlock()
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

func (s *Synchronizer) getExternalMediaDeadline() (time.Duration, bool) {
	s.RLock()
	startTime := s.externalMediaStartTime
	cb := s.config.MediaRunningTime
	maxDelay := s.config.MaxMediaRunningTimeDelay
	s.RUnlock()

	now := mono.Now()

	if startTime.IsZero() && cb != nil {
		if mediaRunningTime, ok := cb(); ok {
			startTime = now.Add(-mediaRunningTime)
			s.Lock()
			if s.externalMediaStartTime.IsZero() {
				s.externalMediaStartTime = startTime
			}
			s.Unlock()
		}
	}

	if startTime.IsZero() {
		return 0, false
	}

	return now.Sub(startTime) - maxDelay, true
}
