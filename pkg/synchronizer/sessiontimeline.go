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

	"github.com/livekit/mediatransportutil/pkg/latency"
	"github.com/livekit/protocol/logger"
)

var errNoSenderReports = errors.New("SessionTimeline: no sender reports received for track")

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
//     sessionPTS = ntpEstimator.RtpToNtp(rtpTS) - participantNtpEpoch + (epochOnReceiverClock - sessionStart)
//     Where:
//     - participantNtpEpoch = NTP time from first SR for this participant
//     - epochOnReceiverClock = participantNtpEpoch + estimatedOWD (maps epoch to receiver clock)
//     - sessionStart = wall-clock time first packet of any track arrived
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

// AddParticipant registers a new participant with the given identity.
func (st *SessionTimeline) AddParticipant(identity string) *ParticipantClock {
	st.mu.Lock()
	defer st.mu.Unlock()

	pc := &ParticipantClock{
		owdEstimator: latency.NewOWDEstimator(latency.OWDEstimatorParamsDefault),
		tracks:       make(map[string]*NtpEstimator),
	}
	st.participants[identity] = pc
	return pc
}

// GetOrAddParticipant returns the ParticipantClock for the given identity,
// creating one if it doesn't exist. This is safe for concurrent use.
func (st *SessionTimeline) GetOrAddParticipant(identity string) *ParticipantClock {
	st.mu.Lock()
	defer st.mu.Unlock()

	if pc, ok := st.participants[identity]; ok {
		return pc
	}

	pc := &ParticipantClock{
		owdEstimator: latency.NewOWDEstimator(latency.OWDEstimatorParamsDefault),
		tracks:       make(map[string]*NtpEstimator),
	}
	st.participants[identity] = pc
	return pc
}

// GetTrackEstimator returns the NTP estimator for a participant's track, or nil.
func (st *SessionTimeline) GetTrackEstimator(identity, trackID string) *NtpEstimator {
	st.mu.RLock()
	defer st.mu.RUnlock()

	pc, ok := st.participants[identity]
	if !ok {
		return nil
	}
	return pc.tracks[trackID]
}

// GetParticipantClock returns the ParticipantClock for a participant, or nil.
func (st *SessionTimeline) GetParticipantClock(identity string) *ParticipantClock {
	st.mu.RLock()
	defer st.mu.RUnlock()

	return st.participants[identity]
}

// RemoveParticipant removes the participant with the given identity.
func (st *SessionTimeline) RemoveParticipant(identity string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.participants, identity)
}

// OnSenderReport processes an RTCP sender report for a participant's track.
// It updates the NTP estimator, OWD estimator, and records the NTP epoch.
// ResetTrack clears the NTP estimator for a track, forcing it to rebuild from
// new sender reports. Used when a stream discontinuity is detected.
func (st *SessionTimeline) ResetTrack(identity, trackID string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	pc, ok := st.participants[identity]
	if !ok {
		return
	}
	if est, ok := pc.tracks[trackID]; ok {
		est.Reset()
	}
}

func (st *SessionTimeline) OnSenderReport(identity, trackID string, clockRate uint32, ntpTime uint64, rtpTimestamp uint32, receivedAt time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()

	pc, ok := st.participants[identity]
	if !ok {
		return
	}

	// Get or create the per-track NTP estimator.
	est, ok := pc.tracks[trackID]
	if !ok {
		est = NewNtpEstimator(clockRate)
		pc.tracks[trackID] = est
	}

	// Feed the SR to the NTP estimator.
	est.OnSenderReport(ntpTime, rtpTimestamp, receivedAt)

	// Convert NTP timestamp to nanoseconds and update OWD.
	senderNtpNanos := ntpTimestampToNanos(ntpTime)
	receiverNanos := receivedAt.UnixNano()
	pc.owdEstimator.Update(senderNtpNanos, receiverNanos)

	// Record the NTP epoch from the first SR for this participant.
	// Note: ntpEpoch cancels out in the GetSessionPTS formula
	// (sessionPTS = ntpTime + OWD - sessionStart), so its exact value
	// doesn't affect the output. It's kept for readability of the formula.
	if !pc.hasEpoch {
		pc.ntpEpoch = nanosToTime(senderNtpNanos)
		pc.hasEpoch = true
	}
}

// GetSessionPTS maps an RTP timestamp for a participant's track to a position
// on the shared session timeline.
//
// The formula is:
//
//	sessionPTS = ntpEstimator.RtpToNtp(rtpTS) - participantNtpEpoch + (epochOnReceiverClock - sessionStart)
//
// Where:
//   - participantNtpEpoch = NTP time from first SR for this participant
//   - epochOnReceiverClock = participantNtpEpoch + estimatedOWD
//   - sessionStart = wall-clock time first packet arrived
func (st *SessionTimeline) GetSessionPTS(identity, trackID string, rtpTimestamp uint32) (time.Duration, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()

	pc, ok := st.participants[identity]
	if !ok {
		return 0, fmt.Errorf("SessionTimeline: unknown participant %q", identity)
	}

	est, ok := pc.tracks[trackID]
	if !ok {
		return 0, errNoSenderReports
	}

	if !est.IsReady() {
		return 0, errNotReady
	}

	if !pc.hasEpoch {
		return 0, errNoSenderReports
	}

	// Map RTP to NTP wall-clock time.
	ntpTime, err := est.RtpToNtp(rtpTimestamp)
	if err != nil {
		return 0, err
	}

	// Compute offset from participant's NTP epoch.
	sinceEpoch := ntpTime.Sub(pc.ntpEpoch)

	// Map the participant's NTP epoch to the receiver's clock.
	estimatedOWD := time.Duration(pc.owdEstimator.EstimatedPropagationDelay())
	epochOnReceiverClock := pc.ntpEpoch.Add(estimatedOWD)

	// Compute the session PTS.
	sessionPTS := sinceEpoch + epochOnReceiverClock.Sub(st.sessionStart)

	if (sessionPTS < 0 || sessionPTS > 24*time.Hour) && st.logger != nil {
		st.logger.Warnw("GetSessionPTS: abnormal result",
			nil,
			"identity", identity,
			"trackID", trackID,
			"rtpTimestamp", rtpTimestamp,
			"ntpTime", ntpTime,
			"ntpEpoch", pc.ntpEpoch,
			"sinceEpoch", sinceEpoch,
			"estimatedOWD", estimatedOWD,
			"epochOnReceiverClock", epochOnReceiverClock,
			"sessionStart", st.sessionStart,
			"sessionPTS", sessionPTS,
		)
	}

	return sessionPTS, nil
}
