// Copyright 2023 LiveKit, Inc.
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
	"math"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/mediatransportutil"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/mono"
)

const (
	cMaxAdjustment = 500 * time.Microsecond
	// Throttle PTS adjustment to a limited amount ina time window.
	// This setting determines how long a certain amount of adjustment
	// throttles the next adjustment.
	//
	// For sample, if a 1ms adjustment is appied at 1%, it means that
	// 1ms is 1% of ajustment window, so the adjustement window is 100ms
	// and next adjustment will not applied for till tha time elapses
	cAdjustmentWindowPercent = 1.0

	cStartTimeAdjustWindow    = 2 * time.Minute
	cStartTimeAdjustThreshold = 5 * time.Second
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . TrackRemote
type TrackRemote interface {
	ID() string
	Codec() webrtc.RTPCodecParameters
	Kind() webrtc.RTPCodecType
	SSRC() webrtc.SSRC
}

type TrackSynchronizer struct {
	sync.Mutex
	sync   *Synchronizer
	track  TrackRemote
	logger logger.Logger
	*rtpConverter

	// config
	maxTsDiff                         time.Duration // maximum acceptable difference between RTP packets
	audioPTSAdjustmentsDisabled       bool          // disable audio packets PTS adjustments on SRs
	preJitterBufferReceiveTimeEnabled bool
	rtcpSenderReportRebaseEnabled     bool
	oldPacketThreshold                time.Duration

	// timing info
	initTime        time.Time
	startTime       time.Time // time first packet was pushed
	startRTP        uint32    // RTP timestamp of PTS 0
	lastTS          uint32    // previous RTP timestamp
	lastTime        time.Time
	lastPTS         time.Duration // previous presentation timestamp
	lastPTSAdjusted time.Duration // previous adjusted presentation timestamp
	maxPTS          time.Duration // maximum valid PTS (set after EOS)

	// offsets
	currentPTSOffset time.Duration // presentation timestamp offset (used for a/v sync)
	desiredPTSOffset time.Duration // desired presentation timestamp offset (used for a/v sync)
	basePTSOffset    time.Duration // component of the desired PTS offset (set initially to preserve initial offset)

	// sender reports
	lastSR uint32
	onSR   func(duration time.Duration)

	nextPTSAdjustmentAt time.Time

	propagationDelayEstimator *OWDEstimator
	totalStartTimeAdjustment  time.Duration
}

func newTrackSynchronizer(s *Synchronizer, track TrackRemote) *TrackSynchronizer {
	t := &TrackSynchronizer{
		sync:                              s,
		track:                             track,
		logger:                            logger.GetLogger().WithValues("trackID", track.ID(), "codec", track.Codec().MimeType),
		rtpConverter:                      newRTPConverter(int64(track.Codec().ClockRate)),
		maxTsDiff:                         s.config.MaxTsDiff,
		audioPTSAdjustmentsDisabled:       s.config.AudioPTSAdjustmentDisabled,
		preJitterBufferReceiveTimeEnabled: s.config.PreJitterBufferReceiveTimeEnabled,
		rtcpSenderReportRebaseEnabled:     s.config.RTCPSenderReportRebaseEnabled,
		oldPacketThreshold:                s.config.OldPacketThreshold,
		nextPTSAdjustmentAt:               mono.Now(),
		propagationDelayEstimator:         NewOWDEstimator(OWDEstimatorParamsDefault),
	}

	return t
}

func (t *TrackSynchronizer) OnSenderReport(f func(drift time.Duration)) {
	t.Lock()
	defer t.Unlock()

	t.onSR = f
}

// Initialize should be called as soon as the first packet is received
func (t *TrackSynchronizer) Initialize(pkt *rtp.Packet) {
	now := mono.Now()
	startedAt := t.sync.getOrSetStartedAt(now.UnixNano())

	t.Lock()
	defer t.Unlock()

	t.currentPTSOffset = time.Duration(now.UnixNano() - startedAt)
	t.desiredPTSOffset = t.currentPTSOffset
	t.basePTSOffset = t.desiredPTSOffset

	t.initTime = now
	t.startRTP = pkt.Timestamp
	t.lastTS = pkt.Timestamp
	t.lastPTS = 0
	t.lastPTSAdjusted = t.currentPTSOffset
	t.logger.Infow(
		"initialized track synchronizer",
		"startRTP", t.startRTP,
		"currentPTSOffset", t.currentPTSOffset,
	)
}

// GetPTS will adjust PTS offsets if necessary
// Packets are expected to be in order
func (t *TrackSynchronizer) GetPTS(pkt jitter.ExtPacket) (time.Duration, error) {
	if t.rtcpSenderReportRebaseEnabled {
		return t.getPTSWithRebase(pkt)
	} else {
		return t.getPTSWithoutRebase(pkt)
	}
}

func (t *TrackSynchronizer) getPTSWithoutRebase(pkt jitter.ExtPacket) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	if t.startTime.IsZero() {
		if t.preJitterBufferReceiveTimeEnabled {
			t.startTime = pkt.ReceivedAt
		} else {
			t.startTime = mono.Now()
		}
		t.logger.Infow(
			"starting track synchronizer",
			"startTime", t.startTime,
			"startRTP", t.startRTP,
			"currentPTSOffset", t.currentPTSOffset,
		)
	}

	ts := pkt.Timestamp
	if ts == t.lastTS {
		return t.lastPTSAdjusted, nil
	}

	if t.preJitterBufferReceiveTimeEnabled {
		if t.isPacketTooOld(pkt.ReceivedAt) {
			return 0, ErrPacketTooOld
		}
	}

	pts := t.lastPTS + t.toDuration(ts-t.lastTS)
	estimatedPTS := time.Since(t.startTime)
	if pts < t.lastPTS || !t.acceptable(pts-estimatedPTS) {
		newStartRTP := ts - t.toRTP(estimatedPTS)
		t.logger.Infow(
			"correcting PTS",
			"currentTS", ts,
			"lastTS", t.lastTS,
			"lastTime", t.lastTime,
			"PTS", pts,
			"lastPTS", t.lastPTS,
			"estimatedPTS", estimatedPTS,
			"offset", pts-estimatedPTS,
			"startRTP", t.startRTP,
			"newStartRTP", newStartRTP,
			"basePTSOffset", t.basePTSOffset,
			"desiredPTSOffset", t.desiredPTSOffset,
			"currentPTSOffset", t.currentPTSOffset,
		)
		pts = estimatedPTS
		t.startRTP = newStartRTP
	}

	if t.shouldAdjustPTS() && mono.Now().After(t.nextPTSAdjustmentAt) {
		prevCurrentPTSOffset := t.currentPTSOffset
		if t.currentPTSOffset > t.desiredPTSOffset {
			t.currentPTSOffset = max(t.currentPTSOffset-cMaxAdjustment, t.desiredPTSOffset)
		} else if t.currentPTSOffset < t.desiredPTSOffset {
			t.currentPTSOffset = min(t.currentPTSOffset+cMaxAdjustment, t.desiredPTSOffset)
		}

		// throttle further adjustment till a window proportional to this adjustment elapses
		throttle := time.Duration(math.Abs(float64(t.currentPTSOffset-prevCurrentPTSOffset)) * 100.0 / float64(cAdjustmentWindowPercent))
		t.nextPTSAdjustmentAt = mono.Now().Add(throttle)

		t.logger.Infow(
			"adjusting PTS offset",
			"currentTS", ts,
			"lastTS", t.lastTS,
			"lastTime", t.lastTime,
			"PTS", pts,
			"lastPTS", t.lastPTS,
			"estimatedPTS", estimatedPTS,
			"ptsOffset", pts-estimatedPTS,
			"startRTP", t.startRTP,
			"lastPTSAdjusted", t.lastPTSAdjusted,
			"basePTSOffset", t.basePTSOffset,
			"desiredPTSOffset", t.desiredPTSOffset,
			"currentPTSOffset", t.currentPTSOffset,
			"prevCurrentPTSOffset", prevCurrentPTSOffset,
			"changeCurrentPTSOffset", t.currentPTSOffset-prevCurrentPTSOffset,
			"throttle", throttle,
		)
	}

	adjusted := pts + t.currentPTSOffset

	// if past end time, return EOF
	if t.maxPTS > 0 && (adjusted > t.maxPTS) {
		return 0, io.EOF
	}

	// update previous values
	t.lastTS = ts
	t.lastTime = mono.Now()
	t.lastPTS = pts
	t.lastPTSAdjusted = adjusted

	return adjusted, nil
}

func (t *TrackSynchronizer) getPTSWithRebase(pkt jitter.ExtPacket) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	if t.startTime.IsZero() {
		t.startTime = pkt.ReceivedAt
		t.logger.Infow(
			"starting track synchronizer",
			"startTime", t.startTime,
			"startRTP", t.startRTP,
			"currentPTSOffset", t.currentPTSOffset,
		)
	}

	ts := pkt.Timestamp
	if ts == t.lastTS {
		return t.lastPTSAdjusted, nil
	}

	// packets are expected in order, just a safety net
	if (ts - t.lastTS) > (1 << 31) {
		t.logger.Infow(
			"dropping out-of-order packet",
			"currentTS", ts,
			"lastTS", t.lastTS,
		)
		return 0, ErrPacketOutOfOrder
	}

	if t.isPacketTooOld(pkt.ReceivedAt) {
		return 0, ErrPacketTooOld
	}

	pts := t.lastPTS + t.toDuration(ts-t.lastTS)
	now := mono.Now()
	estimatedPTS := now.Sub(t.startTime)
	if pts < t.lastPTS || !t.acceptable(pts-estimatedPTS) {
		t.logger.Infow(
			"correcting PTS",
			"currentTS", ts,
			"lastTS", t.lastTS,
			"lastTime", t.lastTime,
			"PTS", pts,
			"lastPTS", t.lastPTS,
			"estimatedPTS", estimatedPTS,
			"offset", pts-estimatedPTS,
			"startRTP", t.startRTP,
			"propagationDelay", t.propagationDelayEstimator,
			"totalStartTimeAdjustment", t.totalStartTimeAdjustment,
			"basePTSOffset", t.basePTSOffset,
			"desiredPTSOffset", t.desiredPTSOffset,
			"currentPTSOffset", t.currentPTSOffset,
		)
		pts = estimatedPTS
	}

	if t.shouldAdjustPTS() && mono.Now().After(t.nextPTSAdjustmentAt) {
		prevCurrentPTSOffset := t.currentPTSOffset
		if t.currentPTSOffset > t.desiredPTSOffset {
			t.currentPTSOffset = max(t.currentPTSOffset-cMaxAdjustment, t.desiredPTSOffset)
		} else if t.currentPTSOffset < t.desiredPTSOffset {
			t.currentPTSOffset = min(t.currentPTSOffset+cMaxAdjustment, t.desiredPTSOffset)
		}

		// throttle further adjustment till a window proportional to this adjustment elapses
		throttle := time.Duration(math.Abs(float64(t.currentPTSOffset-prevCurrentPTSOffset)) * 100.0 / float64(cAdjustmentWindowPercent))
		t.nextPTSAdjustmentAt = mono.Now().Add(throttle)

		t.logger.Infow(
			"adjusting PTS offset",
			"currentTS", ts,
			"lastTS", t.lastTS,
			"lastTime", t.lastTime,
			"PTS", pts,
			"lastPTS", t.lastPTS,
			"estimatedPTS", estimatedPTS,
			"ptsOffset", pts-estimatedPTS,
			"startRTP", t.startRTP,
			"propagationDelay", t.propagationDelayEstimator,
			"totalStartTimeAdjustment", t.totalStartTimeAdjustment,
			"lastPTSAdjusted", t.lastPTSAdjusted,
			"basePTSOffset", t.basePTSOffset,
			"desiredPTSOffset", t.desiredPTSOffset,
			"currentPTSOffset", t.currentPTSOffset,
			"prevCurrentPTSOffset", prevCurrentPTSOffset,
			"changeCurrentPTSOffset", t.currentPTSOffset-prevCurrentPTSOffset,
			"throttle", throttle,
		)
	}

	adjusted := pts + t.currentPTSOffset
	if adjusted < t.lastPTSAdjusted {
		// always move it forward
		t.logger.Infow(
			"propelling PTS forward",
			"currentTS", ts,
			"lastTS", t.lastTS,
			"lastTime", t.lastTime,
			"PTS", pts,
			"lastPTS", t.lastPTS,
			"estimatedPTS", estimatedPTS,
			"ptsOffset", pts-estimatedPTS,
			"startRTP", t.startRTP,
			"propagationDelay", t.propagationDelayEstimator,
			"totalStartTimeAdjustment", t.totalStartTimeAdjustment,
			"adjustedPTS", adjusted,
			"lastPTSAdjusted", t.lastPTSAdjusted,
			"adjustedPTSOffset", adjusted-t.lastPTSAdjusted,
			"basePTSOffset", t.basePTSOffset,
			"desiredPTSOffset", t.desiredPTSOffset,
			"currentPTSOffset", t.currentPTSOffset,
		)
		adjusted = t.lastPTSAdjusted + time.Millisecond
	}

	// if past end time, return EOF
	if t.maxPTS > 0 && (adjusted > t.maxPTS) {
		return 0, io.EOF
	}

	// update previous values
	t.lastTS = ts
	t.lastTime = now
	t.lastPTS = pts
	t.lastPTSAdjusted = adjusted

	return adjusted, nil
}

// onSenderReport handles pts adjustments for a track
func (t *TrackSynchronizer) onSenderReport(pkt *rtcp.SenderReport) {
	if t.rtcpSenderReportRebaseEnabled {
		t.onSenderReportWithRebase(pkt)
	} else {
		t.onSenderReportWithoutRebase(pkt)
	}
}

func (t *TrackSynchronizer) onSenderReportWithoutRebase(pkt *rtcp.SenderReport) {
	t.Lock()
	defer t.Unlock()

	if (t.lastSR != 0 && (pkt.RTPTime-t.lastSR) > (1<<31)) || pkt.RTPTime == t.lastSR || t.startTime.IsZero() {
		return
	}

	var pts time.Duration
	if pkt.RTPTime > t.lastTS {
		pts = t.lastPTS + t.toDuration(pkt.RTPTime-t.lastTS)
	} else {
		pts = t.lastPTS - t.toDuration(t.lastTS-pkt.RTPTime)
	}
	if !t.acceptable(pts - time.Since(t.startTime)) {
		return
	}

	offset := mediatransportutil.NtpTime(pkt.NTPTime).Time().Sub(t.startTime.Add(pts))
	if t.onSR != nil {
		t.onSR(offset)
	}

	if !t.acceptable(offset) {
		return
	}

	t.desiredPTSOffset = t.basePTSOffset + offset
	t.lastSR = pkt.RTPTime
}

func (t *TrackSynchronizer) onSenderReportWithRebase(pkt *rtcp.SenderReport) {
	t.Lock()
	defer t.Unlock()

	// estimate propagation, i. e. one way delay based on NTP time in the report and when it is received
	receivedAt := mono.UnixNano()
	estimatedPropagationDelay := time.Duration(t.updatePropagationDelay(pkt, receivedAt))

	if (t.lastSR != 0 && (pkt.RTPTime-t.lastSR) > (1<<31)) || pkt.RTPTime == t.lastSR || t.startTime.IsZero() {
		return
	}

	var ptsSR time.Duration
	if (pkt.RTPTime - t.lastTS) < (1 << 31) {
		ptsSR = t.lastPTS + t.toDuration(pkt.RTPTime-t.lastTS)
	} else {
		ptsSR = t.lastPTS - t.toDuration(t.lastTS-pkt.RTPTime)
	}
	if !t.acceptable(ptsSR - time.Since(t.startTime)) {
		return
	}

	// rebase the sender report NTP time to local clock
	rebasedSenderTime := mediatransportutil.NtpTime(pkt.NTPTime).Time().Add(estimatedPropagationDelay)

	// adjust the start time based on estimated propagation delay
	// to make subsequent PTS calculations more accurate
	adjustmentStartTimeNano := t.maybeAdjustStartTime(pkt, rebasedSenderTime.UnixNano())

	// it is possible that first sender report is late, adjust down propagation delay if that is the case
	estimatedPropagationDelay = time.Duration(t.propagationDelayEstimator.InitialAdjustment(adjustmentStartTimeNano))
	// rebase the sender report NTP time to local clock once again after initial adjustment if any
	rebasedSenderTime = mediatransportutil.NtpTime(pkt.NTPTime).Time().Add(estimatedPropagationDelay)

	// offset is based on local clock
	offset := rebasedSenderTime.Sub(t.startTime.Add(ptsSR))
	if t.onSR != nil {
		t.onSR(offset)
	}
	if offset > 20*time.Millisecond || offset < -20*time.Millisecond {
		t.logger.Infow(
			"high offset",
			"lastTS", t.lastTS,
			"lastTime", t.lastTime,
			"lastPTS", t.lastPTS,
			"rebasedSenderTime", rebasedSenderTime,
			"PTS_SR", ptsSR,
			"startRTP", t.startRTP,
			"propagationDelay", t.propagationDelayEstimator,
			"totalStartTimeAdjustment", t.totalStartTimeAdjustment,
			"offset", offset,
			"startTime", t.startTime,
			"ptsSRTime", t.startTime.Add(ptsSR),
			"sr", pkt,
			"estimatedPropagationDelay", estimatedPropagationDelay,
			"basePTSOffset", t.basePTSOffset,
			"desiredPTSOffset", t.desiredPTSOffset,
			"currentPTSOffset", t.currentPTSOffset,
		)
	} else {
		t.logger.Infow(
			"acceptable offset",
			"lastTS", t.lastTS,
			"lastTime", t.lastTime,
			"lastPTS", t.lastPTS,
			"rebasedSenderTime", rebasedSenderTime,
			"PTS_SR", ptsSR,
			"startRTP", t.startRTP,
			"propagationDelay", t.propagationDelayEstimator,
			"totalStartTimeAdjustment", t.totalStartTimeAdjustment,
			"offset", offset,
			"startTime", t.startTime,
			"ptsSRTime", t.startTime.Add(ptsSR),
			"sr", pkt,
			"estimatedPropagationDelay", estimatedPropagationDelay,
			"basePTSOffset", t.basePTSOffset,
			"desiredPTSOffset", t.desiredPTSOffset,
			"currentPTSOffset", t.currentPTSOffset,
		)
	}

	if !t.acceptable(offset) {
		return
	}

	t.desiredPTSOffset = t.basePTSOffset + offset
	t.lastSR = pkt.RTPTime
}

func (t *TrackSynchronizer) updatePropagationDelay(sr *rtcp.SenderReport, receivedAt int64) int64 {
	senderClockTime := mediatransportutil.NtpTime(sr.NTPTime).Time().UnixNano()
	estimatedPropagationDelay, stepChange := t.propagationDelayEstimator.Update(
		senderClockTime,
		receivedAt,
	)
	if stepChange {
		t.logger.Debugw(
			"propagation delay step change",
			"propagationDelayEstimator", t.propagationDelayEstimator,
			"senderClockTime", senderClockTime,
			"receivedAt", receivedAt,
			"sr", sr,
		)
	}

	return estimatedPropagationDelay
}

func (t *TrackSynchronizer) maybeAdjustStartTime(sr *rtcp.SenderReport, rebasedReceivedAt int64) int64 {
	nowNano := mono.UnixNano()
	startTimeNano := t.startTime.UnixNano()
	if time.Duration(nowNano-startTimeNano) > cStartTimeAdjustWindow {
		return 0
	}

	// for some time after the start, adjust time of first packet.
	// Helps improve accuracy of expected timestamp calculation.
	// Adjusting only one way, i. e. if the first sample experienced
	// abnormal delay (maybe due to pacing or maybe due to queuing
	// in some network element along the way), push back first time
	// to an earlier instance.
	timeSinceReceive := time.Duration(nowNano - rebasedReceivedAt)
	nowTS := sr.RTPTime + t.toRTP(timeSinceReceive)
	samplesDiff := nowTS - t.startRTP
	if int32(samplesDiff) < 0 {
		// out-of-order, pre-start, skip
		t.logger.Debugw(
			"no adjustment due to pre-staart report",
			"startTime", t.startTime,
			"nowTS", nowTS,
			"startTS", t.startRTP,
			"sr", sr,
			"timeSinceReceive", timeSinceReceive,
			"samplesDiff", int32(samplesDiff),
		)
		return 0
	}

	samplesDuration := t.toDuration(samplesDiff)
	timeSinceStart := time.Duration(nowNano - startTimeNano)
	now := startTimeNano + timeSinceStart.Nanoseconds()
	adjustedStartTimeNano := now - samplesDuration.Nanoseconds()

	getLoggingFields := func() []interface{} {
		return []interface{}{
			"startTime", t.startTime,
			"nowTime", time.Unix(0, now),
			"before", t.startTime,
			"after", time.Unix(0, adjustedStartTimeNano),
			"adjustment", time.Duration(startTimeNano - adjustedStartTimeNano),
			"nowTS", nowTS,
			"startTS", t.startRTP,
			"sr", sr,
			"timeSinceReceive", timeSinceReceive,
			"timeSinceStart", timeSinceStart,
			"samplesDiff", samplesDiff,
			"samplesDuration", samplesDuration,
		}
	}

	if adjustedStartTimeNano < startTimeNano {
		if startTimeNano-adjustedStartTimeNano > cStartTimeAdjustThreshold.Nanoseconds() {
			t.logger.Warnw(
				"adjusting start time, too big, ignoring", nil,
				getLoggingFields()...,
			)
		} else {
			t.logger.Debugw("adjusting start time", getLoggingFields()...)
			t.totalStartTimeAdjustment += time.Duration(startTimeNano - adjustedStartTimeNano)
			t.startTime = time.Unix(0, adjustedStartTimeNano)
		}
	} else {
		t.logger.Debugw("not adjusting start time", getLoggingFields()...)
	}

	return startTimeNano - adjustedStartTimeNano
}

func (t *TrackSynchronizer) acceptable(d time.Duration) bool {
	return d > -t.maxTsDiff && d < t.maxTsDiff
}

func (t *TrackSynchronizer) shouldAdjustPTS() bool {
	if mono.Now().Before(t.nextPTSAdjustmentAt) {
		return false
	}

	adjustmentEnabled := true
	if t.track.Kind() == webrtc.RTPCodecTypeAudio && !t.rtcpSenderReportRebaseEnabled {
		adjustmentEnabled = !t.audioPTSAdjustmentsDisabled
	}
	return adjustmentEnabled && (t.currentPTSOffset != t.desiredPTSOffset)
}

func (t *TrackSynchronizer) isPacketTooOld(packetTime time.Time) bool {
	return t.oldPacketThreshold != 0 && mono.Now().Sub(packetTime) > t.oldPacketThreshold
}

// ---------------------------

type rtpConverter struct {
	ts  uint64
	rtp uint64
}

func newRTPConverter(clockRate int64) *rtpConverter {
	ts := time.Second.Nanoseconds()
	for _, i := range []int64{10, 3, 2} {
		for ts%i == 0 && clockRate%i == 0 {
			ts /= i
			clockRate /= i
		}
	}

	return &rtpConverter{ts: uint64(ts), rtp: uint64(clockRate)}
}

func (c *rtpConverter) toDuration(rtpDuration uint32) time.Duration {
	return time.Duration(uint64(rtpDuration) * c.ts / c.rtp)
}

func (c *rtpConverter) toRTP(duration time.Duration) uint32 {
	return uint32(duration.Nanoseconds() * int64(c.rtp) / int64(c.ts))
}
