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

package synchronizer

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
)

var (
	errNoSenderReports    = errors.New("SessionTimeline: no sender reports received for track")
	errNoSessionStart     = errors.New("SessionTimeline: session start time not set")
	errAbnormalSessionPTS = errors.New("SessionTimeline: session PTS outside plausible range")
)

// OWD and regression noise put a participant's first frames slightly before a session start set by another track
const maxNegativeSessionPTS = time.Second

// deliberate max-session-length policy, not derived from the RTP wrap period
const maxSessionPTS = 24 * time.Hour

// SessionTimeline establishes a shared recording timeline and maps each
// participant's NTP clock domain onto it using OWD (one-way delay)
// normalization. This is the key component that fixes cross-participant
// misalignment.
//
// Algorithm:
//  1. Each SR provides a pair: (senderNtpTime, receivedAtWallClock). The
//     difference is the one-way delay (OWD).
//  2. Using the OWDEstimator, estimate each participant's OWD. The min
//     observed OWD approximates true propagation delay.
//  3. To map a participant's RTP timestamp to the session timeline:
//     sessionPTS = ntpTime + estimatedOWD - sessionStart
type SessionTimeline struct {
	mu           sync.RWMutex
	logger       logger.Logger
	participants map[string]*ParticipantClock
	sessionStart time.Time
	hasStart     bool
}

// NewSessionTimeline creates a new SessionTimeline.
func NewSessionTimeline(l logger.Logger) *SessionTimeline {
	return &SessionTimeline{
		logger:       l,
		participants: make(map[string]*ParticipantClock),
	}
}

// SetSessionStart sets the session start time (wall-clock time when the first
// packet of any track arrived at the receiver).
func (st *SessionTimeline) SetSessionStart(t time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.sessionStart = t
	st.hasStart = true
}

// SetSessionStartIfNotSet atomically sets the session start time only if it
// has not yet been set. Returns true on the first successful set, false
// otherwise. This is used by SyncEngine.initializeIfNeeded to order the
// timeline state ahead of the public startedAt publication, eliminating the
// window in which startedAt is visible as non-zero but hasStart is still
// false (which would silently drop SR callbacks and force one packet to
// wall-clock PTS on a different track).
func (st *SessionTimeline) SetSessionStartIfNotSet(t time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.hasStart {
		return false
	}
	st.sessionStart = t
	st.hasStart = true
	return true
}

// GetSessionStartNanos returns the session start time in Unix nanoseconds,
// or 0 if not yet set. This is the authoritative read for callers that need
// a stable value — reading SyncEngine.startedAt directly is racy during the
// brief window after the winning initializeIfNeeded goroutine has set the
// timeline but not yet published startedAt atomically.
func (st *SessionTimeline) GetSessionStartNanos() int64 {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if !st.hasStart {
		return 0
	}
	return st.sessionStart.UnixNano()
}

// AddParticipant registers a new participant with the given participantID.
func (st *SessionTimeline) AddParticipant(participantID string) *ParticipantClock {
	st.mu.Lock()
	defer st.mu.Unlock()

	pc := NewParticipantClock(st.logger, participantID)
	st.participants[participantID] = pc
	return pc
}

// GetOrAddParticipant returns the ParticipantClock for the given participantID,
// creating one if it doesn't exist. This is safe for concurrent use.
func (st *SessionTimeline) GetOrAddParticipant(participantID string) *ParticipantClock {
	st.mu.Lock()
	defer st.mu.Unlock()

	if pc, ok := st.participants[participantID]; ok {
		return pc
	}

	pc := NewParticipantClock(st.logger, participantID)
	st.participants[participantID] = pc
	return pc
}

// GetParticipantClock returns the ParticipantClock for a participant, or nil.
func (st *SessionTimeline) GetParticipantClock(participantID string) *ParticipantClock {
	st.mu.RLock()
	defer st.mu.RUnlock()

	return st.participants[participantID]
}

// RemoveParticipant removes the participant with the given participantID.
func (st *SessionTimeline) RemoveParticipant(participantID string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	delete(st.participants, participantID)
}

// ResetTrack clears the NTP estimator for a track, forcing it to rebuild from
// new sender reports. Used when a stream discontinuity is detected.
func (st *SessionTimeline) ResetTrack(participantID, trackID string) {
	st.mu.RLock()
	pc, ok := st.participants[participantID]
	st.mu.RUnlock()

	if ok {
		pc.ResetTrack(trackID)
	}
}

// OnSenderReport processes an RTCP sender report for a participant's track.
// It delegates to the ParticipantClock to update the NTP estimator, OWD
// estimator, and NTP epoch.
func (st *SessionTimeline) OnSenderReport(participantID, trackID string, clockRate uint32, ntpTime uint64, rtpTimestamp uint32, receivedAt time.Time) {
	st.mu.RLock()
	pc, ok := st.participants[participantID]
	st.mu.RUnlock()

	if !ok {
		return
	}

	pc.OnSenderReport(trackID, clockRate, ntpTime, rtpTimestamp, receivedAt)
}

// GetSessionPTS maps an RTP timestamp for a participant's track to a position
// on the shared session timeline. It returns errAbnormalSessionPTS when the
// result falls outside the plausible range.
//
// The formula is: sessionPTS = ntpTime + estimatedOWD - sessionStart
func (st *SessionTimeline) GetSessionPTS(participantID, trackID string, rtpTimestamp uint32) (time.Duration, error) {
	return st.sessionPTS(participantID, trackID, rtpTimestamp, true)
}

// sampleSessionPTS is GetSessionPTS for callers that only read the value; it leaves
// abnormal-episode state untouched so a diagnostic cannot perturb the packet path's.
func (st *SessionTimeline) sampleSessionPTS(participantID, trackID string, rtpTimestamp uint32) (time.Duration, error) {
	return st.sessionPTS(participantID, trackID, rtpTimestamp, false)
}

func (st *SessionTimeline) sessionPTS(participantID, trackID string, rtpTimestamp uint32, note bool) (time.Duration, error) {
	st.mu.RLock()
	if !st.hasStart {
		st.mu.RUnlock()
		return 0, errNoSessionStart
	}
	pc, ok := st.participants[participantID]
	sessionStart := st.sessionStart
	st.mu.RUnlock()

	if !ok {
		return 0, fmt.Errorf("SessionTimeline: unknown participant %q", participantID)
	}

	receiverTime, err := pc.RtpToReceiverClock(trackID, rtpTimestamp)
	if err != nil {
		return 0, err
	}

	var sessionPTS time.Duration
	var abnormal bool
	if note {
		sessionPTS, abnormal = pc.NoteSessionPTS(trackID, rtpTimestamp, receiverTime, sessionStart)
	} else {
		sessionPTS, abnormal = classifySessionPTS(receiverTime, sessionStart)
	}
	if abnormal {
		return 0, errAbnormalSessionPTS
	}

	return sessionPTS, nil
}
