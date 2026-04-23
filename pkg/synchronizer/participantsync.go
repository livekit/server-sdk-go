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
	// slewRatePerSecond is the maximum adjustment slew: 5ms per second of real time.
	slewRatePerSecond = 5 * time.Millisecond

	// deadbandThreshold is the minimum |offset| before any correction is applied.
	deadbandThreshold = 5 * time.Millisecond
)

// trackEntry holds per-track state within a ParticipantSync.
type trackEntry struct {
	mediaType  MediaType
	estimator  *NtpEstimator
	adjustment time.Duration // current playout delay adjustment
}

// ParticipantSync compares NTP estimates across a participant's audio and video
// tracks to compute A/V playout delay adjustments with time-based slew rate
// limiting. This is the equivalent of Chrome's StreamSynchronization.
//
// Audio is the reference track; video absorbs the correction.
type ParticipantSync struct {
	mu     sync.Mutex
	tracks map[string]*trackEntry

	// lastSessionTime records the session time from the most recent
	// updateAdjustments call, used to compute elapsed time for slew limiting.
	lastSessionTime time.Duration
	initialized     bool // true after first updateAdjustments call

	// targetOffset is the desired total video adjustment (negative means
	// video should be delayed relative to audio).
	targetOffset time.Duration

	// currentOffset tracks the slew-limited offset applied so far.
	currentOffset time.Duration
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
	ps.tracks[trackID] = &trackEntry{
		mediaType: mediaType,
		estimator: estimator,
	}
}

// RemoveTrack removes a track and resets its adjustment.
func (ps *ParticipantSync) RemoveTrack(trackID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.tracks, trackID)
	// Reset sync state since we may no longer have both audio and video.
	ps.targetOffset = 0
	ps.currentOffset = 0
	// Clear adjustments on remaining tracks.
	for _, entry := range ps.tracks {
		entry.adjustment = 0
	}
}

// OnSenderReport is called when new SR data arrives for a track. It triggers
// recomputation of the A/V offset target.
func (ps *ParticipantSync) OnSenderReport(trackID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.recomputeTarget()
}

// GetAdjustment returns the current playout delay adjustment for a track.
// Returns zero if the track is not registered or estimators are not ready.
func (ps *ParticipantSync) GetAdjustment(trackID string) time.Duration {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	entry, ok := ps.tracks[trackID]
	if !ok {
		return 0
	}
	return entry.adjustment
}

// updateAdjustments is called periodically (by SyncEngine) to drive the
// time-based slew toward the target offset. sessionTime is the elapsed
// session time (monotonically increasing).
func (ps *ParticipantSync) updateAdjustments(sessionTime time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if !ps.initialized {
		ps.lastSessionTime = sessionTime
		ps.initialized = true
		// Recompute on first call in case SRs arrived before the first tick.
		ps.recomputeTarget()
		ps.applyAdjustments()
		return
	}

	elapsed := sessionTime - ps.lastSessionTime
	ps.lastSessionTime = sessionTime

	if elapsed <= 0 {
		return
	}

	// Compute the maximum slew for this interval.
	maxSlew := time.Duration(float64(slewRatePerSecond) * float64(elapsed) / float64(time.Second))

	// Move currentOffset toward targetOffset, bounded by maxSlew.
	diff := ps.targetOffset - ps.currentOffset
	if diff > 0 {
		if diff > maxSlew {
			diff = maxSlew
		}
		ps.currentOffset += diff
	} else if diff < 0 {
		if -diff > maxSlew {
			diff = -maxSlew
		}
		ps.currentOffset += diff
	}

	ps.applyAdjustments()
}

// recomputeTarget recalculates the target A/V offset from the latest NTP
// samples of the audio and video estimators.
func (ps *ParticipantSync) recomputeTarget() {
	audioNTP, audioOK := ps.latestNTP(MediaTypeAudio)
	videoNTP, videoOK := ps.latestNTP(MediaTypeVideo)

	if !audioOK || !videoOK {
		return
	}

	// offset = video NTP - audio NTP.
	// If positive, video's NTP is ahead of audio's, meaning video needs to be
	// delayed (negative adjustment) to align with audio.
	offset := videoNTP - audioNTP

	// Apply deadband: if the offset is small enough, treat it as zero.
	if offset > -deadbandThreshold && offset < deadbandThreshold {
		ps.targetOffset = 0
		return
	}

	// Video absorbs the correction: negate the offset so that a positive NTP
	// difference becomes a negative (delay) adjustment on video.
	ps.targetOffset = -offset
}

// latestNTP returns the NTP time of the most recent SR sample for the first
// ready estimator of the given media type. The second return value is false if
// no ready estimator of that type exists.
func (ps *ParticipantSync) latestNTP(mt MediaType) (time.Duration, bool) {
	for _, entry := range ps.tracks {
		if entry.mediaType != mt || entry.estimator == nil || !entry.estimator.IsReady() {
			continue
		}
		if entry.estimator.sampleLen == 0 {
			continue
		}

		// The most recent sample is at (sampleHead - 1 + maxSRSamples) % maxSRSamples.
		idx := (entry.estimator.sampleHead - 1 + maxSRSamples) % maxSRSamples
		s := entry.estimator.samples[idx]
		return time.Duration(s.ntpNanos), true
	}
	return 0, false
}

// applyAdjustments distributes the current slew-limited offset to the
// appropriate tracks. Audio gets zero; video gets the correction.
func (ps *ParticipantSync) applyAdjustments() {
	for _, entry := range ps.tracks {
		switch entry.mediaType {
		case MediaTypeAudio:
			entry.adjustment = 0
		case MediaTypeVideo:
			entry.adjustment = ps.currentOffset
		}
	}
}
