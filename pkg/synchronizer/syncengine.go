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
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/livekit/media-sdk/jitter"

	"github.com/livekit/protocol/utils/rtputil"
)

const (
	// transitionSlewRatePerSecond is the rate at which the wall-clock→NTP
	// transition correction is absorbed: 5ms per second of real time.
	transitionSlewRatePerSecond = 5 * time.Millisecond

	// wallClockSanityThreshold is the maximum divergence between RTP-derived PTS
	// and wall-clock PTS before falling back to wall clock.
	wallClockSanityThreshold = 5 * time.Second
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

// SyncEngine orchestrates NtpEstimator, ParticipantSync, and SessionTimeline
// to provide cross-participant alignment and per-participant A/V lip sync.
// It implements the Sync interface.
type SyncEngine struct {
	mu       sync.Mutex
	timeline *SessionTimeline
	tracks   map[uint32]*syncEngineTrack // keyed by SSRC
	trackIDs map[string]*syncEngineTrack // keyed by track ID

	startedAt atomic.Int64
	endedAt   atomic.Int64

	enableStartGate bool
	onStarted       func()

	mediaRunningTime     func() (time.Duration, bool)
	mediaRunningTimeLock sync.RWMutex
}

// NewSyncEngine creates a new SyncEngine with the given options.
func NewSyncEngine(opts ...SyncEngineOption) *SyncEngine {
	e := &SyncEngine{
		timeline: NewSessionTimeline(),
		tracks:   make(map[uint32]*syncEngineTrack),
		trackIDs: make(map[string]*syncEngineTrack),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// AddTrack registers a new track and returns a TrackSync handle.
func (e *SyncEngine) AddTrack(track TrackRemote, identity string) TrackSync {
	ssrc := uint32(track.SSRC())
	clockRate := track.Codec().ClockRate

	e.mu.Lock()
	defer e.mu.Unlock()

	// Ensure the participant exists in the timeline.
	e.timeline.mu.Lock()
	pc, ok := e.timeline.participants[identity]
	if !ok {
		e.timeline.mu.Unlock()
		pc = e.timeline.AddParticipant(identity)
		e.timeline.mu.Lock()
	}

	// Auto-register the track with ParticipantSync using a placeholder estimator.
	mt := MediaTypeAudio
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		mt = MediaTypeVideo
	}
	placeholder := NewNtpEstimator(clockRate)
	pc.participantSync.SetTrackEstimator(track.ID(), mt, placeholder)
	e.timeline.mu.Unlock()

	st := &syncEngineTrack{
		engine:    e,
		track:     track,
		identity:  identity,
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
	ssrc := uint32(st.track.SSRC())
	delete(e.tracks, ssrc)
	delete(e.trackIDs, trackID)
	e.mu.Unlock()

	st.Close()
}

// OnRTCP processes an RTCP packet, dispatching sender reports to the appropriate
// track's NTP estimator and ParticipantSync.
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
	identity := st.identity
	trackID := st.track.ID()
	clockRate := st.track.Codec().ClockRate
	e.mu.Unlock()

	now := time.Now()

	// Feed the SR to the session timeline (updates NTP estimator + OWD).
	e.timeline.OnSenderReport(identity, trackID, clockRate, sr.NTPTime, sr.RTPTime, now)

	// Wire up ParticipantSync: get the track's estimator from timeline and update it.
	e.timeline.mu.RLock()
	pc, pcOK := e.timeline.participants[identity]
	if pcOK {
		pt, ptOK := pc.tracks[trackID]
		if ptOK {
			mt := MediaTypeAudio
			if st.track.Kind() == webrtc.RTPCodecTypeVideo {
				mt = MediaTypeVideo
			}
			pc.participantSync.SetTrackEstimator(trackID, mt, pt.estimator)
			pc.participantSync.OnSenderReport(trackID)

			// Compute elapsed session time for slew limiting.
			startedAt := e.startedAt.Load()
			if startedAt > 0 {
				elapsed := time.Duration(now.UnixNano() - startedAt)
				pc.participantSync.updateAdjustments(elapsed)
			}
		}
	}
	e.timeline.mu.RUnlock()

	// Call onSR callback if set.
	st.mu.Lock()
	onSR := st.onSR
	st.mu.Unlock()

	if onSR != nil {
		// Compute drift as the difference between NTP-derived time and wall clock elapsed.
		ntpNanos := ntpTimestampToNanos(sr.NTPTime)
		ntpTime := nanosToTime(ntpNanos)
		startedAt := e.startedAt.Load()
		if startedAt > 0 {
			sessionStart := time.Unix(0, startedAt)
			expectedElapsed := now.Sub(sessionStart)
			ntpElapsed := ntpTime.Sub(sessionStart)
			drift := ntpElapsed - expectedElapsed
			onSR(drift)
		}
	}
}

// End signals the end of the session and sets drain ceilings on all tracks.
func (e *SyncEngine) End() {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Find the maximum adjusted PTS across all tracks.
	var maxPTS time.Duration
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

// --- syncEngineTrack ---

// syncEngineTrack implements TrackSync for a single track within a SyncEngine.
type syncEngineTrack struct {
	engine    *SyncEngine
	track     TrackRemote
	identity  string
	converter *rtputil.RTPConverter
	startGate startGate // from start_gate.go, nil if not enabled

	mu              sync.Mutex
	startTime       time.Time
	sessionOffset   time.Duration // offset from session start to this track's start
	lastTS          uint32
	lastPTS         time.Duration
	lastPTSAdjusted time.Duration
	initialized     bool
	closed          bool

	// NTP transition
	ntpTransitioned bool
	transitionSlew  time.Duration
	lastSlewTime    time.Time // wall-clock time of last slew step

	// drain
	maxPTS    time.Duration
	maxPTSSet bool

	onSR func(drift time.Duration)
}

// PrimeForStart implements TrackSync. It buffers packets through the optional
// start gate and initializes the track on the first valid packet.
func (st *syncEngineTrack) PrimeForStart(pkt jitter.ExtPacket) ([]jitter.ExtPacket, int, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.initialized || st.startGate == nil {
		if !st.initialized {
			st.initializeLocked(pkt)
		}
		return []jitter.ExtPacket{pkt}, 0, true
	}

	ready, dropped, done := st.startGate.Push(pkt)
	if !done {
		return nil, dropped, false
	}

	if len(ready) == 0 {
		ready = []jitter.ExtPacket{pkt}
	}

	if !st.initialized {
		st.initializeLocked(ready[0])
	}

	return ready, dropped, true
}

// initializeLocked sets the track's start time and registers with the engine.
// Caller must hold st.mu.
func (st *syncEngineTrack) initializeLocked(pkt jitter.ExtPacket) {
	receivedAt := pkt.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}

	st.startTime = receivedAt
	st.lastTS = pkt.Timestamp
	st.initialized = true

	// Initialize the engine's session start time.
	sessionStart := st.engine.initializeIfNeeded(receivedAt)
	st.sessionOffset = time.Duration(receivedAt.UnixNano() - sessionStart)
}

// GetPTS implements TrackSync. It computes the presentation timestamp for a packet
// using the NTP-grounded timeline when available, falling back to wall clock otherwise.
func (st *syncEngineTrack) GetPTS(pkt jitter.ExtPacket) (time.Duration, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.closed {
		return 0, io.EOF
	}

	if !st.initialized {
		st.initializeLocked(pkt)
	}

	ts := pkt.Timestamp

	// Same RTP timestamp as last packet: return same PTS (same frame).
	if ts == st.lastTS && st.lastPTSAdjusted > 0 {
		return st.lastPTSAdjusted, nil
	}

	// Step 1: Try NTP-grounded PTS from SessionTimeline.
	ntpPTS, ntpErr := st.engine.timeline.GetSessionPTS(st.identity, st.track.ID(), ts)

	var pts time.Duration
	if ntpErr != nil {
		// Step 2: Fall back to wall-clock PTS.
		pts = st.wallClockPTS(pkt)
	} else {
		// Step 3: On first successful NTP PTS, compute transition correction.
		if !st.ntpTransitioned {
			wallPTS := st.wallClockPTS(pkt)
			st.transitionSlew = wallPTS - ntpPTS
			st.ntpTransitioned = true
		}
		pts = ntpPTS
	}

	// Step 4: Apply transition slew (absorb gradually toward zero, time-based).
	if st.transitionSlew != 0 {
		pts += st.transitionSlew

		now := pkt.ReceivedAt
		if now.IsZero() {
			now = time.Now()
		}
		if !st.lastSlewTime.IsZero() {
			elapsed := now.Sub(st.lastSlewTime)
			maxStep := time.Duration(float64(transitionSlewRatePerSecond) * elapsed.Seconds())
			if maxStep > 0 {
				if st.transitionSlew > 0 {
					st.transitionSlew -= maxStep
					if st.transitionSlew < 0 {
						st.transitionSlew = 0
					}
				} else {
					st.transitionSlew += maxStep
					if st.transitionSlew > 0 {
						st.transitionSlew = 0
					}
				}
			}
		}
		st.lastSlewTime = now
	}

	// Step 5: Apply ParticipantSync A/V adjustment.
	st.engine.timeline.mu.RLock()
	if pc, ok := st.engine.timeline.participants[st.identity]; ok {
		adj := pc.participantSync.GetAdjustment(st.track.ID())
		pts += adj
	}
	st.engine.timeline.mu.RUnlock()

	// Step 6: Enforce monotonicity.
	if pts < st.lastPTSAdjusted+time.Millisecond && st.lastPTSAdjusted > 0 {
		pts = st.lastPTSAdjusted + time.Millisecond
	}

	// Step 7: Enforce drain ceiling.
	if st.maxPTSSet && pts > st.maxPTS {
		return 0, io.EOF
	}

	// Update state.
	st.lastTS = ts
	st.lastPTS = pts // the raw PTS before adjustment (for wall clock computation)
	st.lastPTSAdjusted = pts

	return pts, nil
}

// wallClockPTS computes a PTS based on wall-clock timing and RTP deltas.
func (st *syncEngineTrack) wallClockPTS(pkt jitter.ExtPacket) time.Duration {
	ts := pkt.Timestamp

	// Same RTP timestamp as last packet: same frame.
	if st.lastTS == ts && st.lastPTS > 0 {
		return st.lastPTS
	}

	// Wall-clock elapsed since this track started, plus session offset
	wallElapsed := pkt.ReceivedAt.Sub(st.startTime) + st.sessionOffset

	// If we have a previous timestamp, use RTP delta for more precision.
	if st.lastPTS > 0 {
		rtpDelta := ts - st.lastTS
		rtpDerived := st.lastPTS + st.converter.ToDuration(rtpDelta)

		// Sanity check: if RTP-derived PTS diverges from wall-clock by > 5s, use wall clock.
		diff := rtpDerived - wallElapsed
		if diff < 0 {
			diff = -diff
		}
		if diff <= wallClockSanityThreshold {
			return rtpDerived
		}
	}

	// Use wall-clock elapsed, ensuring non-negative.
	if wallElapsed < 0 {
		wallElapsed = 0
	}
	return wallElapsed
}

// OnSenderReport implements TrackSync. It stores a callback invoked on sender reports.
func (st *syncEngineTrack) OnSenderReport(f func(drift time.Duration)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.onSR = f
}

// LastPTSAdjusted implements TrackSync.
func (st *syncEngineTrack) LastPTSAdjusted() time.Duration {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastPTSAdjusted
}

// Close implements TrackSync.
func (st *syncEngineTrack) Close() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.closed = true
}
