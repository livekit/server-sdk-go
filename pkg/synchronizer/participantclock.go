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
)

// ParticipantClock holds OWD and NTP estimation state for a single participant.
type ParticipantClock struct {
	mu           sync.Mutex
	owdEstimator *latency.OWDEstimator
	tracks       map[string]*NtpEstimator
	ntpEpoch     time.Time // NTP time from first SR
	hasEpoch     bool
}

// SetTrackEstimator registers or updates the NtpEstimator for a given track.
func (pc *ParticipantClock) SetTrackEstimator(trackID string, estimator *NtpEstimator) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.tracks[trackID] = estimator
}

// RemoveTrack removes a track.
func (pc *ParticipantClock) RemoveTrack(trackID string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	delete(pc.tracks, trackID)
}
