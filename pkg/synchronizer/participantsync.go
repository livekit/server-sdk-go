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
)

// MediaType distinguishes audio and video tracks for A/V sync purposes.
type MediaType int

const (
	MediaTypeAudio MediaType = iota
	MediaTypeVideo
)

const (
	// slewRatePerSecond is the maximum rate at which PTS corrections are absorbed.
	slewRatePerSecond = 5 * time.Millisecond

	// deadbandThreshold is the minimum |correction| before slew smoothing kicks in.
	deadbandThreshold = 5 * time.Millisecond
)

// trackEntry holds per-track state within a ParticipantSync.
type trackEntry struct {
	mediaType MediaType
	estimator *NtpEstimator
}

// ParticipantSync holds per-participant sender report state and track metadata.
// PTS jump smoothing is handled directly in syncEngineTrack.GetPTS using a
// per-track correction that decays at the slew rate.
type ParticipantSync struct {
	mu     sync.Mutex
	tracks map[string]*trackEntry
}

// NewParticipantSync creates a new ParticipantSync instance.
func NewParticipantSync() *ParticipantSync {
	return &ParticipantSync{
		tracks: make(map[string]*trackEntry),
	}
}

// SetTrackEstimator registers or updates the NtpEstimator for a given track.
func (ps *ParticipantSync) SetTrackEstimator(trackID string, mediaType MediaType, estimator *NtpEstimator) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if existing, ok := ps.tracks[trackID]; ok {
		existing.estimator = estimator
		existing.mediaType = mediaType
		return
	}

	ps.tracks[trackID] = &trackEntry{
		mediaType: mediaType,
		estimator: estimator,
	}
}

// RemoveTrack removes a track.
func (ps *ParticipantSync) RemoveTrack(trackID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.tracks, trackID)
}

// OnSenderReport is called when new SR data arrives for a track.
func (ps *ParticipantSync) OnSenderReport(trackID string) {
	// SR data is processed by SessionTimeline's NtpEstimator.
	// Jump detection and smoothing happen in syncEngineTrack.GetPTS.
}
