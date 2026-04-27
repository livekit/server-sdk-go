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
	"sync"
	"time"

	"github.com/livekit/mediatransportutil/pkg/latency"
	"github.com/livekit/protocol/logger"
)

// ParticipantClock holds OWD and NTP estimation state for a single participant.
type ParticipantClock struct {
	mu           sync.Mutex
	logger       logger.Logger
	owdEstimator *latency.OWDEstimator
	tracks       map[string]*NtpEstimator
	ntpEpoch     time.Time // NTP time from first SR
	hasEpoch     bool
}

// NewParticipantClock creates a new ParticipantClock.
func NewParticipantClock(l logger.Logger) *ParticipantClock {
	return &ParticipantClock{
		logger:       l,
		owdEstimator: latency.NewOWDEstimator(latency.OWDEstimatorParamsDefault),
		tracks:       make(map[string]*NtpEstimator),
	}
}

// OnSenderReport processes an RTCP sender report for a track.
// It updates the NTP estimator, OWD estimator, and records the NTP epoch.
func (pc *ParticipantClock) OnSenderReport(trackID string, clockRate uint32, ntpTime uint64, rtpTimestamp uint32, receivedAt time.Time) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	est, ok := pc.tracks[trackID]
	if !ok {
		est = NewNtpEstimator(clockRate)
		pc.tracks[trackID] = est
	}

	result := est.OnSenderReport(ntpTime, rtpTimestamp, receivedAt)
	if result == SROutlier && pc.logger != nil {
		pc.logger.Warnw("sender report rejected as outlier", nil,
			"trackID", trackID,
			"rtpTimestamp", rtpTimestamp,
			"ntpTime", ntpTime,
		)
	}
	if result != SRAccepted {
		return
	}

	senderNtpNanos := ntpTimestampToNanos(ntpTime)
	pc.owdEstimator.Update(senderNtpNanos, receivedAt.UnixNano())

	if !pc.hasEpoch {
		pc.ntpEpoch = nanosToTime(senderNtpNanos)
		pc.hasEpoch = true
	}
}

// RtpToReceiverClock maps an RTP timestamp to a time on the receiver's clock.
// The result is ntpTime + estimatedOWD, which places the sender's NTP time
// into the receiver's clock domain.
func (pc *ParticipantClock) RtpToReceiverClock(trackID string, rtpTimestamp uint32) (time.Time, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	est, ok := pc.tracks[trackID]
	if !ok {
		return time.Time{}, errNoSenderReports
	}

	if !est.IsReady() {
		return time.Time{}, errNotReady
	}

	if !pc.hasEpoch {
		return time.Time{}, errNoSenderReports
	}

	ntpTime, err := est.RtpToNtp(rtpTimestamp)
	if err != nil {
		return time.Time{}, err
	}

	estimatedOWD := time.Duration(pc.owdEstimator.EstimatedPropagationDelay())
	return ntpTime.Add(estimatedOWD), nil
}

// ResetTrack clears the NTP estimator for a track, forcing it to rebuild
// from new sender reports. Used when a stream discontinuity is detected.
func (pc *ParticipantClock) ResetTrack(trackID string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if est, ok := pc.tracks[trackID]; ok {
		est.Reset()
	}
}

// RemoveTrack removes a track.
func (pc *ParticipantClock) RemoveTrack(trackID string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	delete(pc.tracks, trackID)
}

// HasTrack returns true if the participant has a track with the given ID.
func (pc *ParticipantClock) HasTrack(trackID string) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	_, ok := pc.tracks[trackID]
	return ok
}
