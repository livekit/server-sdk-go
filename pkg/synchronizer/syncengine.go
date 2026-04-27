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

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/rtputil"
)

const (
	// transitionSlewRatePerSecond is the rate at which the wall-clock→NTP
	// transition correction is absorbed: 5ms per second of real time.
	transitionSlewRatePerSecond = 5 * time.Millisecond

	// wallClockSanityThreshold is the maximum divergence between RTP-derived PTS
	// and wall-clock PTS before falling back to wall clock in wallClockPTS().
	wallClockSanityThreshold = 5 * time.Second

	// ntpTrustThreshold is the maximum allowed divergence between NTP-derived PTS
	// and wall-clock PTS. If NTP disagrees with wall clock by more than this,
	// the NTP data is suspect (bad SRs, clock jumps, nonsensical timing) and
	// we clamp to wall clock. This prevents bad publishers from dragging PTS far
	// from reality.
	ntpTrustThreshold = 500 * time.Millisecond

	// maxTimelyPacketAge is how long a track can be behind the pipeline deadline
	// before its PTS is force-corrected forward.
	maxTimelyPacketAge = 10 * time.Second

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

	// high-water mark for removed tracks, so End() includes their PTS
	maxRemovedPTS time.Duration

	logger             logger.Logger
	enableStartGate    bool
	oldPacketThreshold time.Duration
	onStarted          func()

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
func (e *SyncEngine) AddTrack(track TrackRemote, identity string) TrackSync {
	ssrc := uint32(track.SSRC())
	clockRate := track.Codec().ClockRate

	e.mu.Lock()
	defer e.mu.Unlock()

	// Ensure the participant exists in the timeline.
	pc := e.timeline.GetOrAddParticipant(identity)

	// Auto-register the track with ParticipantSync using a placeholder estimator.
	mt := MediaTypeAudio
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		mt = MediaTypeVideo
	}
	placeholder := NewNtpEstimator(clockRate, e.logger)
	pc.participantSync.SetTrackEstimator(track.ID(), mt, placeholder)

	st := &syncEngineTrack{
		engine:    e,
		track:     track,
		identity:  identity,
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

	st.logger.Infow("track removed", "lastPTS", st.lastPTSAdjusted)
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
	if estimator := e.timeline.GetTrackEstimator(identity, trackID); estimator != nil {
		mt := MediaTypeAudio
		if st.track.Kind() == webrtc.RTPCodecTypeVideo {
			mt = MediaTypeVideo
		}
		if ps := e.timeline.GetParticipantSync(identity); ps != nil {
			ps.SetTrackEstimator(trackID, mt, estimator)
			ps.OnSenderReport(trackID)
		}
	}

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
			sessionPTS, err := e.timeline.GetSessionPTS(identity, trackID, sr.RTPTime)
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

func (e *SyncEngine) getTrackLogger(track TrackRemote) logger.Logger {
	if e.logger != nil {
		return e.logger.WithValues("trackID", track.ID(), "kind", track.Kind().String())
	}
	return logger.GetLogger().WithValues("trackID", track.ID(), "kind", track.Kind().String(), "syncEngine", true)
}

// --- syncEngineTrack ---

// syncEngineTrack implements TrackSync for a single track within a SyncEngine.
type syncEngineTrack struct {
	engine    *SyncEngine
	track     TrackRemote
	identity  string
	logger    logger.Logger
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

	// NTP transition and smoothing
	ntpTransitioned bool
	transitionSlew  time.Duration
	lastSlewPTS     time.Duration // PTS at which slew was last updated
	lastNtpPTS      time.Duration // last raw NTP PTS (before corrections), for jump detection
	ntpCorrection   time.Duration // smoothing correction for SR-induced NTP jumps

	// pipeline time feedback
	lastTimelyPacket time.Time

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
	st.lastTimelyPacket = receivedAt
	st.initialized = true

	// Initialize the engine's session start time.
	sessionStart := st.engine.initializeIfNeeded(receivedAt)
	st.sessionOffset = time.Duration(receivedAt.UnixNano() - sessionStart)

	st.logger.Infow("initialized track",
		"startTime", st.startTime,
		"sessionOffset", st.sessionOffset,
		"rtpTS", pkt.Timestamp,
	)
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

	// Drop packets older than threshold.
	if st.engine.oldPacketThreshold > 0 && !pkt.ReceivedAt.IsZero() {
		if time.Since(pkt.ReceivedAt) > st.engine.oldPacketThreshold {
			return 0, ErrPacketTooOld
		}
	}

	// Step 1: Try NTP-grounded PTS from SessionTimeline.
	rawNtpPTS, ntpErr := st.engine.timeline.GetSessionPTS(st.identity, st.track.ID(), ts)

	wallPTS := st.wallClockPTS(pkt)

	// Step 2: Detect discontinuities and NTP regression jumps on RAW NTP PTS.
	// This operates before any corrections to avoid feedback loops.
	rtpDelta := ts - st.lastTS
	rtpDeltaDuration := st.converter.ToDuration(rtpDelta)

	if st.lastTS != 0 && rtpDeltaDuration >= 30*time.Second {
		// Discontinuity: stream restart, SSRC reuse with new RTP offset, or massive gap.
		st.engine.timeline.ResetTrack(st.identity, st.track.ID())
		st.lastNtpPTS = 0
		st.ntpCorrection = 0
		st.ntpTransitioned = false
		st.transitionSlew = 0
		st.lastSlewPTS = 0
		st.logger.Warnw("stream discontinuity detected, resetting NTP state", nil,
			"rtpDelta", rtpDelta,
			"rtpDeltaDuration", rtpDeltaDuration,
		)
	} else if ntpErr == nil && st.lastNtpPTS > 0 && rtpDelta > 0 {
		// Detect regression jumps: compare raw NTP PTS against expected.
		expectedRawNtpPTS := st.lastNtpPTS + rtpDeltaDuration
		jump := rawNtpPTS - expectedRawNtpPTS
		if jump > deadbandThreshold || jump < -deadbandThreshold {
			st.ntpCorrection -= jump
			st.logger.Debugw("NTP regression jump detected",
				"jump", jump,
				"ntpCorrection", st.ntpCorrection,
			)
		}
	}
	if ntpErr == nil {
		st.lastNtpPTS = rawNtpPTS // Always track raw NTP PTS, never corrected
	}

	// Step 3: Compute final PTS with corrections.
	var pts time.Duration
	if ntpErr != nil {
		pts = wallPTS
	} else {
		// Apply NTP jump correction.
		pts = rawNtpPTS + st.ntpCorrection

		// Clamp corrected PTS to within trust threshold of wall clock.
		diff := pts - wallPTS
		if diff > ntpTrustThreshold || diff < -ntpTrustThreshold {
			st.logger.Warnw("NTP PTS exceeds trust threshold, clamping to wall clock", nil,
				"rawNtpPTS", rawNtpPTS,
				"ntpCorrection", st.ntpCorrection,
				"wallPTS", wallPTS,
				"diff", diff,
			)
			pts = wallPTS
		}

		// On first successful NTP PTS, compute transition correction.
		if !st.ntpTransitioned {
			st.transitionSlew = wallPTS - pts
			st.ntpTransitioned = true
			st.logger.Infow("NTP transition",
				"wallPTS", wallPTS,
				"ntpPTS", rawNtpPTS,
				"transitionSlew", st.transitionSlew,
			)
		}
	}

	// Compute PTS delta for slew rate calculations.
	var slewPTSDelta time.Duration
	if st.lastSlewPTS > 0 {
		slewPTSDelta = pts - st.lastSlewPTS
	}

	// Step 4: Apply transition slew (absorb gradually toward zero).
	if st.transitionSlew != 0 {
		pts += st.transitionSlew

		if slewPTSDelta > 0 {
			maxStep := time.Duration(float64(transitionSlewRatePerSecond) * slewPTSDelta.Seconds())
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

	// Decay ntpCorrection toward zero via slew.
	if st.ntpCorrection != 0 {
		if slewPTSDelta > 0 {
			maxStep := time.Duration(float64(slewRatePerSecond) * slewPTSDelta.Seconds())
			if st.ntpCorrection > 0 {
				st.ntpCorrection -= maxStep
				if st.ntpCorrection < 0 {
					st.ntpCorrection = 0
				}
			} else {
				st.ntpCorrection += maxStep
				if st.ntpCorrection > 0 {
					st.ntpCorrection = 0
				}
			}
		}
	}

	st.lastSlewPTS = pts

	// Step 6: Pipeline time feedback — if the track has fallen behind the
	// pipeline's deadline for too long, force-correct PTS forward.
	if deadline, ok := st.engine.getMediaDeadline(); ok && st.engine.maxMediaRunningTimeDelay > 0 {
		limit := deadline - st.engine.maxMediaRunningTimeDelay
		if pts < limit {
			if time.Since(st.lastTimelyPacket) > maxTimelyPacketAge {
				oldPTS := pts
				pts = deadline - st.engine.maxMediaRunningTimeDelay/2
				st.logger.Warnw("force-correcting PTS forward, track behind pipeline deadline", nil,
					"oldPTS", oldPTS,
					"newPTS", pts,
					"deadline", deadline,
					"behindBy", limit-oldPTS,
				)
			}
		} else {
			st.lastTimelyPacket = time.Now()
		}
	}

	// Step 7: Enforce monotonicity.
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
