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
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/livekit/media-sdk/jitter"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/rtputil"
)

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

	// Audio tracks with external drift compensation (e.g., tempo controller) skip
	// NTP PTS corrections — drift is handled by resampling, not PTS adjustment.
	// NTP regression still runs (via OnSenderReport) for drift measurement.
	useWallClockOnly := st.engine.audioDriftCompensated && st.track.Kind() == webrtc.RTPCodecTypeAudio

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
	} else if !useWallClockOnly && ntpErr == nil && st.lastNtpPTS > 0 && rtpDelta > 0 {
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
	if ntpErr != nil || useWallClockOnly {
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
