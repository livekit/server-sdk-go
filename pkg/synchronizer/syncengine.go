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
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/rtputil"
)

const (
	// defaultOldPacketThreshold is the default age after which packets are dropped.
	defaultOldPacketThreshold = 500 * time.Millisecond
)

// SyncEngineOption configures a SyncEngine.
type SyncEngineOption func(*SyncEngine)

// WithSyncEngineOnStarted sets a callback invoked once the first track is initialized.
func WithSyncEngineOnStarted(f func()) SyncEngineOption {
	return func(e *SyncEngine) {
		e.onStarted = f
	}
}

// WithSyncEngineStartGate enables the burst-estimation start gate on all tracks.
func WithSyncEngineStartGate() SyncEngineOption {
	return func(e *SyncEngine) {
		e.enableStartGate = true
	}
}

// WithSyncEngineOldPacketThreshold sets the age after which packets are dropped.
// Zero disables the check.
func WithSyncEngineOldPacketThreshold(d time.Duration) SyncEngineOption {
	return func(e *SyncEngine) {
		e.oldPacketThreshold = d
	}
}

// WithSyncEngineMediaRunningTime sets the initial media running time provider and max delay.
// If a track's PTS falls behind the deadline by more than maxDelay for >10s, PTS is force-corrected.
func WithSyncEngineMediaRunningTime(mediaRunningTime func() (time.Duration, bool), maxDelay time.Duration) SyncEngineOption {
	return func(e *SyncEngine) {
		e.mediaRunningTime = mediaRunningTime
		e.maxMediaRunningTimeDelay = maxDelay
	}
}

// WithSyncEngineLogger sets the logger for the sync engine and all sub-components.
func WithSyncEngineLogger(l logger.Logger) SyncEngineOption {
	return func(e *SyncEngine) {
		e.logger = l
	}
}

// WithSyncEngineAudioDriftCompensated signals that audio drift is handled
// externally (e.g., by a tempo controller) and the sync engine should not
// apply NTP PTS corrections to audio tracks. NTP regression still runs for
// drift measurement and reporting.
func WithSyncEngineAudioDriftCompensated() SyncEngineOption {
	return func(e *SyncEngine) {
		e.audioDriftCompensated = true
	}
}

// SyncEngine orchestrates NtpEstimator, ParticipantClock, and SessionTimeline
// to provide cross-participant alignment and per-participant A/V lip sync.
// It implements the Sync interface.
type SyncEngine struct {
	mu       sync.Mutex
	timeline *SessionTimeline
	tracks   map[uint32]*syncEngineTrack // keyed by SSRC
	trackIDs map[string]*syncEngineTrack // keyed by track ID

	startedAt atomic.Int64
	endedAt   atomic.Int64

	// high-water mark for removed tracks, so End() includes their PTS
	maxRemovedPTS time.Duration

	logger                logger.Logger
	enableStartGate       bool
	oldPacketThreshold    time.Duration
	audioDriftCompensated bool // audio drift handled externally (e.g., tempo controller)
	onStarted             func()

	mediaRunningTime         func() (time.Duration, bool)
	maxMediaRunningTimeDelay time.Duration
	mediaRunningTimeLock     sync.RWMutex
}

// NewSyncEngine creates a new SyncEngine with the given options.
func NewSyncEngine(opts ...SyncEngineOption) *SyncEngine {
	e := &SyncEngine{
		tracks:             make(map[uint32]*syncEngineTrack),
		trackIDs:           make(map[string]*syncEngineTrack),
		oldPacketThreshold: defaultOldPacketThreshold,
	}
	for _, opt := range opts {
		opt(e)
	}
	e.timeline = NewSessionTimeline(e.logger)
	return e
}

// AddTrack registers a new track and returns a TrackSync handle.
func (e *SyncEngine) AddTrack(track TrackRemote, participantID string) TrackSync {
	ssrc := uint32(track.SSRC())
	clockRate := track.Codec().ClockRate

	e.mu.Lock()
	defer e.mu.Unlock()

	// Ensure the participant exists in the timeline.
	e.timeline.GetOrAddParticipant(participantID)

	st := &syncEngineTrack{
		engine:    e,
		track:     track,
		participantID:  participantID,
		logger:    e.getTrackLogger(track),
		converter: rtputil.NewRTPConverter(int64(clockRate)),
	}

	if e.enableStartGate {
		st.startGate = newStartGate(clockRate, track.Kind(), nil)
	}

	e.tracks[ssrc] = st
	e.trackIDs[track.ID()] = st

	return st
}

// RemoveTrack removes a track by track ID.
func (e *SyncEngine) RemoveTrack(trackID string) {
	e.mu.Lock()
	st, ok := e.trackIDs[trackID]
	if !ok {
		e.mu.Unlock()
		return
	}

	// Preserve removed track's PTS high-water mark so End() includes it.
	st.mu.Lock()
	if st.lastPTSAdjusted > e.maxRemovedPTS {
		e.maxRemovedPTS = st.lastPTSAdjusted
	}
	st.mu.Unlock()

	ssrc := uint32(st.track.SSRC())
	delete(e.tracks, ssrc)
	delete(e.trackIDs, trackID)
	e.mu.Unlock()

	// Clean up track from participant, and remove the participant from the
	// timeline if this was their last track.
	participantID := st.participantID
	if pc := e.timeline.GetParticipantClock(participantID); pc != nil {
		pc.RemoveTrack(trackID)
	}
	if !e.hasTracksForParticipant(participantID) {
		e.timeline.RemoveParticipant(participantID)
	}

	st.logger.Infow("track removed", "lastPTS", st.lastPTSAdjusted)
	st.Close()
}

// OnRTCP processes an RTCP packet, dispatching sender reports to the appropriate
// track's NTP estimator and ParticipantClock.
func (e *SyncEngine) OnRTCP(packet rtcp.Packet) {
	sr, ok := packet.(*rtcp.SenderReport)
	if !ok {
		return
	}

	e.mu.Lock()
	st, ok := e.tracks[sr.SSRC]
	if !ok {
		e.mu.Unlock()
		return
	}
	participantID := st.participantID
	trackID := st.track.ID()
	clockRate := st.track.Codec().ClockRate
	e.mu.Unlock()

	now := time.Now()

	// Feed the SR to the session timeline (updates NTP estimator + OWD).
	e.timeline.OnSenderReport(participantID, trackID, clockRate, sr.NTPTime, sr.RTPTime, now)

	// Call onSR callback if set.
	st.mu.Lock()
	onSR := st.onSR
	st.mu.Unlock()

	if onSR != nil {
		// Compute drift using OWD-normalized session PTS (not raw NTP, which
		// includes the sender's clock offset and would produce phantom drift
		// if the sender's NTP clock adjusts during the recording).
		startedAt := e.startedAt.Load()
		if startedAt > 0 {
			sessionPTS, err := e.timeline.GetSessionPTS(participantID, trackID, sr.RTPTime)
			if err == nil {
				sessionStart := time.Unix(0, startedAt)
				expectedElapsed := now.Sub(sessionStart)
				drift := sessionPTS - expectedElapsed
				st.logger.Debugw("sender report",
					"drift", drift,
					"sessionPTS", sessionPTS,
					"expectedElapsed", expectedElapsed,
				)
				onSR(drift)
			}
		}
	}
}

// End signals the end of the session and sets drain ceilings on all tracks.
func (e *SyncEngine) End() {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Start from the high-water mark of removed tracks.
	maxPTS := e.maxRemovedPTS
	for _, st := range e.tracks {
		st.mu.Lock()
		if st.lastPTSAdjusted > maxPTS {
			maxPTS = st.lastPTSAdjusted
		}
		st.mu.Unlock()
	}

	startedAt := e.startedAt.Load()
	if startedAt > 0 {
		e.endedAt.Store(startedAt + int64(maxPTS))
	} else {
		e.endedAt.Store(time.Now().UnixNano())
	}

	// Set drain ceiling on all tracks.
	for _, st := range e.tracks {
		st.mu.Lock()
		st.maxPTS = maxPTS
		st.maxPTSSet = true
		st.mu.Unlock()
	}
}

// GetStartedAt returns the start timestamp in nanoseconds, or 0 if not started.
func (e *SyncEngine) GetStartedAt() int64 {
	return e.startedAt.Load()
}

// GetEndedAt returns the end timestamp in nanoseconds, or 0 if not ended.
func (e *SyncEngine) GetEndedAt() int64 {
	return e.endedAt.Load()
}

// SetMediaRunningTime sets the external media running time provider.
func (e *SyncEngine) SetMediaRunningTime(mediaRunningTime func() (time.Duration, bool)) {
	e.mediaRunningTimeLock.Lock()
	e.mediaRunningTime = mediaRunningTime
	e.mediaRunningTimeLock.Unlock()
}

// getMediaDeadline returns the current pipeline deadline, or false if unavailable.
func (e *SyncEngine) getMediaDeadline() (time.Duration, bool) {
	e.mediaRunningTimeLock.RLock()
	fn := e.mediaRunningTime
	e.mediaRunningTimeLock.RUnlock()
	if fn == nil {
		return 0, false
	}
	return fn()
}

// initializeIfNeeded sets the session start time and fires the onStarted callback
// on the first track initialization. Returns the startedAt value.
func (e *SyncEngine) initializeIfNeeded(receivedAt time.Time) int64 {
	nano := receivedAt.UnixNano()
	if e.startedAt.CompareAndSwap(0, nano) {
		e.timeline.SetSessionStart(receivedAt)
		if e.onStarted != nil {
			e.onStarted()
		}
	}
	return e.startedAt.Load()
}

// hasTracksForParticipant returns true if any remaining track belongs to the
// given participant participantID. Caller must NOT hold e.mu.
func (e *SyncEngine) hasTracksForParticipant(participantID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, st := range e.tracks {
		if st.participantID == participantID {
			return true
		}
	}
	return false
}

func (e *SyncEngine) getTrackLogger(track TrackRemote) logger.Logger {
	if e.logger != nil {
		return e.logger.WithValues("trackID", track.ID(), "kind", track.Kind().String())
	}
	return logger.GetLogger().WithValues("trackID", track.ID(), "kind", track.Kind().String(), "syncEngine", true)
}
