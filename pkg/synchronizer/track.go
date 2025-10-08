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
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"go.uber.org/zap/zapcore"

	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/mediatransportutil"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/mono"
)

const (
	cStartTimeAdjustWindow    = 2 * time.Minute
	cStartTimeAdjustThreshold = 5 * time.Second

	cHighDriftLoggingThreshold = 20 * time.Millisecond
	cGapHistogramNumBins       = 101
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
	startGate startGate

	// config
	maxTsDiff                         time.Duration // maximum acceptable difference between RTP packets
	maxDriftAdjustment                time.Duration // maximum drift adjustment at a time
	driftAdjustmentWindowPercent      float64
	audioPTSAdjustmentsDisabled       bool // disable audio packets PTS adjustments on SRs
	preJitterBufferReceiveTimeEnabled bool
	rtcpSenderReportRebaseEnabled     bool
	oldPacketThreshold                time.Duration
	enableStartGate                   bool

	// timing info
	startTime        time.Time // time at initialization --> this should be when first packet is received
	startRTP         uint32    // RTP timestamp of first packet
	firstTime        time.Time // time at which first packet was pushed
	lastTS           uint32    // previous RTP timestamp
	lastSN           uint16    // previous RTP sequence number
	lastTime         time.Time
	lastPTS          time.Duration // previous presentation timestamp
	lastPTSAdjusted  time.Duration // previous adjusted presentation timestamp
	lastTSOldDropped uint32        // previous dropped RTP timestamp due to old packet
	maxPTS           time.Duration // maximum valid PTS (set after EOS)

	// offsets
	currentPTSOffset           time.Duration // presentation timestamp offset (used for a/v sync)
	desiredPTSOffset           time.Duration // desired presentation timestamp offset (used for a/v sync)
	basePTSOffset              time.Duration // component of the desired PTS offset (set initially to preserve initial offset)
	totalPTSAdjustmentPositive time.Duration
	totalPTSAdjustmentNegative time.Duration

	// sender reports
	lastSR *augmentedSenderReport
	onSR   func(duration time.Duration)

	nextPTSAdjustmentAt time.Time

	propagationDelayEstimator *OWDEstimator
	totalStartTimeAdjustment  time.Duration
	startTimeAdjustResidual   time.Duration
	initialized               bool

	stats stats
}

func newTrackSynchronizer(s *Synchronizer, track TrackRemote) *TrackSynchronizer {
	t := &TrackSynchronizer{
		sync:                              s,
		track:                             track,
		logger:                            logger.GetLogger().WithValues("trackID", track.ID(), "codec", track.Codec().MimeType),
		rtpConverter:                      newRTPConverter(int64(track.Codec().ClockRate)),
		maxTsDiff:                         s.config.MaxTsDiff,
		maxDriftAdjustment:                s.config.MaxDriftAdjustment,
		driftAdjustmentWindowPercent:      s.config.DriftAdjustmentWindowPercent,
		audioPTSAdjustmentsDisabled:       s.config.AudioPTSAdjustmentDisabled,
		preJitterBufferReceiveTimeEnabled: s.config.PreJitterBufferReceiveTimeEnabled,
		rtcpSenderReportRebaseEnabled:     s.config.RTCPSenderReportRebaseEnabled,
		oldPacketThreshold:                s.config.OldPacketThreshold,
		enableStartGate:                   s.config.EnableStartGate,
		nextPTSAdjustmentAt:               mono.Now(),
		propagationDelayEstimator:         NewOWDEstimator(OWDEstimatorParamsDefault),
	}

	if s.config.EnableStartGate {
		t.startGate = newStartGate(track.Codec().ClockRate, track.Kind(), t.logger)
	}

	return t
}

func (t *TrackSynchronizer) OnSenderReport(f func(drift time.Duration)) {
	t.Lock()
	defer t.Unlock()

	t.onSR = f
}

// PrimeForStart buffers incoming packets until pacing stabilizes, initializing the
// track synchronizer automatically once a suitable sequence has been observed.
// It returns the packets that should be forwarded, the number of packets
// dropped while waiting, and a boolean indicating whether the track is ready to
// process samples. After the gate finishes, there is no need to call the API again.
func (t *TrackSynchronizer) PrimeForStart(pkt jitter.ExtPacket) ([]jitter.ExtPacket, int, bool) {
	if t.initialized || t.startGate == nil {
		if !t.initialized {
			t.initialize(pkt)
		}
		return []jitter.ExtPacket{pkt}, 0, true
	}

	ready, dropped, done := t.startGate.Push(pkt)
	if !done {
		return nil, dropped, false
	}

	if len(ready) == 0 {
		ready = []jitter.ExtPacket{pkt}
	}

	if !t.initialized {
		t.initialize(ready[0])
	}

	return ready, dropped, true
}

// Initialize should be called as soon as the first packet is received
func (t *TrackSynchronizer) Initialize(pkt *rtp.Packet) {
	t.initialize(jitter.ExtPacket{
		Packet:     pkt,
		ReceivedAt: mono.Now(),
	})
}

func (t *TrackSynchronizer) initialize(extPkt jitter.ExtPacket) {
	receivedAt := extPkt.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = mono.Now()
	}

	t.Lock()
	synchronizer := t.sync
	t.Unlock()

	startedAt := receivedAt.UnixNano()
	firstStartedAt := startedAt
	if synchronizer != nil {
		firstStartedAt = synchronizer.getOrSetStartedAt(startedAt)
	}

	t.Lock()
	defer t.Unlock()
	if t.initialized {
		return
	}

	t.currentPTSOffset = time.Duration(startedAt - firstStartedAt)
	t.desiredPTSOffset = t.currentPTSOffset
	t.basePTSOffset = t.desiredPTSOffset

	t.startTime = receivedAt
	t.startRTP = extPkt.Packet.Timestamp
	t.lastPTS = 0
	t.lastPTSAdjusted = t.currentPTSOffset
	t.initialized = true
	t.logger.Infow(
		"initialized track synchronizer",
		"state", t,
		"SSRC", t.track.SSRC(),
		"maxTsDiff", t.maxTsDiff,
		"maxDriftAdjustment", t.maxDriftAdjustment,
		"driftAdjustmentWindowPercent", t.driftAdjustmentWindowPercent,
		"audioPTSAdjustmentDisabled", t.audioPTSAdjustmentsDisabled,
		"preJitterBufferReceiveTimeEnabled", t.preJitterBufferReceiveTimeEnabled,
		"rtcpSenderReportRebaseEnabled", t.rtcpSenderReportRebaseEnabled,
		"oldPacketThreshold", t.oldPacketThreshold,
		"enableStartGate", t.enableStartGate,
	)
}

func (t *TrackSynchronizer) Close() {
	t.Lock()
	defer t.Unlock()

	t.sync = nil
	t.logger.Infow("closing track synchronizer", "state", t)
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

	now := mono.Now()
	var pktReceiveTime time.Time
	if t.preJitterBufferReceiveTimeEnabled {
		pktReceiveTime = pkt.ReceivedAt
	} else {
		pktReceiveTime = now
	}
	if t.firstTime.IsZero() {
		t.firstTime = now
		t.logger.Infow(
			"starting track synchronizer",
			"state", t,
			"pktReceiveTime", pkt.ReceivedAt,
			"startDelay", t.firstTime.Sub(pkt.ReceivedAt),
		)
	} else {
		t.updateGapHistogram(pkt.SequenceNumber - t.lastSN)
	}
	t.lastSN = pkt.SequenceNumber

	ts := pkt.Timestamp

	// if first packet of a frame was accepted,
	// accept all packets of the frame even if they are old
	if ts == t.lastTS {
		t.stats.numEmitted++
		return t.lastPTSAdjusted, nil
	}

	if t.preJitterBufferReceiveTimeEnabled {
		// if first packet a frame was too old and dropped,
		// drop all packets of the frame irrespective of whether they are old or not
		if ts == t.lastTSOldDropped || t.isPacketTooOld(pkt.ReceivedAt) {
			t.lastTSOldDropped = ts
			t.stats.numDroppedOld++
			t.logger.Infow(
				"dropping old packet",
				"currentTS", ts,
				"receivedAt", pkt.ReceivedAt,
				"now", mono.Now(),
				"age", mono.Now().Sub(pkt.ReceivedAt),
				"state", t,
			)
			return 0, ErrPacketTooOld
		}
	}

	estimatedPTS := pktReceiveTime.Sub(t.startTime)

	var pts time.Duration
	if t.lastPTS == 0 {
		// start with estimated PTS to absorb any start latency
		pts = max(time.Nanosecond, estimatedPTS) // prevent lastPTS from being stuck at 0
	} else {
		pts = t.lastPTS + t.toDuration(ts-t.lastTS)
	}

	if pts < t.lastPTS || !t.acceptable(pts-estimatedPTS) {
		newStartRTP := ts - t.toRTP(estimatedPTS)
		t.logger.Infow(
			"correcting PTS",
			"currentTS", ts,
			"PTS", pts,
			"estimatedPTS", estimatedPTS,
			"offset", pts-estimatedPTS,
			"newStartRTP", newStartRTP,
			"state", t,
		)
		pts = estimatedPTS
		t.startRTP = newStartRTP
	}

	if t.shouldAdjustPTS() {
		prevCurrentPTSOffset := t.currentPTSOffset
		if t.currentPTSOffset > t.desiredPTSOffset {
			t.currentPTSOffset = max(t.currentPTSOffset-t.maxDriftAdjustment, t.desiredPTSOffset)
			t.totalPTSAdjustmentNegative += prevCurrentPTSOffset - t.currentPTSOffset
		} else if t.currentPTSOffset < t.desiredPTSOffset {
			t.currentPTSOffset = min(t.currentPTSOffset+t.maxDriftAdjustment, t.desiredPTSOffset)
			t.totalPTSAdjustmentPositive += t.currentPTSOffset - prevCurrentPTSOffset
		}

		// throttle further adjustment till a window proportional to this adjustment elapses
		throttle := time.Duration(0)
		if t.driftAdjustmentWindowPercent > 0.0 {
			throttle = time.Duration(math.Abs(float64(t.currentPTSOffset-prevCurrentPTSOffset)) * 100.0 / t.driftAdjustmentWindowPercent)
		}
		t.nextPTSAdjustmentAt = mono.Now().Add(throttle)

		t.logger.Infow(
			"adjusting PTS offset",
			"currentTS", ts,
			"PTS", pts,
			"estimatedPTS", estimatedPTS,
			"ptsOffset", pts-estimatedPTS,
			"prevCurrentPTSOffset", prevCurrentPTSOffset,
			"changeCurrentPTSOffset", t.currentPTSOffset-prevCurrentPTSOffset,
			"throttle", throttle,
			"state", t,
		)
	}

	adjusted := pts + t.currentPTSOffset

	// if past end time, return EOF
	if t.maxPTS > 0 && (adjusted > t.maxPTS) {
		t.stats.numDroppedEOF++
		return 0, io.EOF
	}

	// update previous values
	t.lastTS = ts
	t.lastTime = now
	t.lastPTS = pts
	t.lastPTSAdjusted = adjusted

	t.stats.numEmitted++
	if adjusted < 0 {
		t.stats.numNegativePTS++
	}
	return adjusted, nil
}

func (t *TrackSynchronizer) getPTSWithRebase(pkt jitter.ExtPacket) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	now := mono.Now()
	if t.firstTime.IsZero() {
		t.firstTime = now
		t.logger.Infow(
			"starting track synchronizer",
			"state", t,
			"pktReceiveTime", pkt.ReceivedAt,
			"startDelay", t.firstTime.Sub(pkt.ReceivedAt),
		)
	} else {
		t.updateGapHistogram(pkt.SequenceNumber - t.lastSN)
	}
	t.lastSN = pkt.SequenceNumber

	ts := pkt.Timestamp

	// if first packet of a frame was accepted,
	// accept all packets of the frame even if they are old
	if ts == t.lastTS {
		t.stats.numEmitted++
		return t.lastPTSAdjusted, nil
	}

	// packets are expected in order, just a safety net
	if t.lastTS != 0 && (ts-t.lastTS) > (1<<31) {
		t.stats.numDroppedOutOfOrder++
		t.logger.Infow(
			"dropping out-of-order packet",
			"currentTS", ts,
			"state", t,
		)
		return 0, ErrPacketOutOfOrder
	}

	// if first packet a frame was too old and dropped,
	// drop all packets of the frame irrespective of whether they are old or not
	if ts == t.lastTSOldDropped || t.isPacketTooOld(pkt.ReceivedAt) {
		t.lastTSOldDropped = ts
		t.stats.numDroppedOld++
		t.logger.Infow(
			"dropping old packet",
			"currentTS", ts,
			"receivedAt", pkt.ReceivedAt,
			"now", now,
			"age", now.Sub(pkt.ReceivedAt),
			"state", t,
		)
		return 0, ErrPacketTooOld
	}

	estimatedPTS := pkt.ReceivedAt.Sub(t.startTime)

	var pts time.Duration
	if t.lastPTS == 0 {
		// start with estimated PTS to absorb any start latency
		pts = max(time.Nanosecond, estimatedPTS) // prevent lastPTS from being stuck at 0
	} else {
		pts = t.lastPTS + t.toDuration(ts-t.lastTS)
	}

	if pts < t.lastPTS || !t.acceptable(pts-estimatedPTS) {
		t.logger.Infow(
			"correcting PTS",
			"currentTS", ts,
			"PTS", pts,
			"estimatedPTS", estimatedPTS,
			"offset", pts-estimatedPTS,
			"state", t,
		)
		pts = estimatedPTS
	}

	if t.shouldAdjustPTS() {
		prevCurrentPTSOffset := t.currentPTSOffset
		if t.currentPTSOffset > t.desiredPTSOffset {
			t.currentPTSOffset = max(t.currentPTSOffset-t.maxDriftAdjustment, t.desiredPTSOffset)
			t.totalPTSAdjustmentNegative += prevCurrentPTSOffset - t.currentPTSOffset
		} else if t.currentPTSOffset < t.desiredPTSOffset {
			t.currentPTSOffset = min(t.currentPTSOffset+t.maxDriftAdjustment, t.desiredPTSOffset)
			t.totalPTSAdjustmentPositive += t.currentPTSOffset - prevCurrentPTSOffset
		}

		// throttle further adjustment till a window proportional to this adjustment elapses
		throttle := time.Duration(0)
		if t.driftAdjustmentWindowPercent > 0.0 {
			throttle = time.Duration(math.Abs(float64(t.currentPTSOffset-prevCurrentPTSOffset)) * 100.0 / t.driftAdjustmentWindowPercent)
		}
		t.nextPTSAdjustmentAt = mono.Now().Add(throttle)

		t.logger.Infow(
			"adjusting PTS offset",
			"currentTS", ts,
			"PTS", pts,
			"estimatedPTS", estimatedPTS,
			"ptsOffset", pts-estimatedPTS,
			"prevCurrentPTSOffset", prevCurrentPTSOffset,
			"changeCurrentPTSOffset", t.currentPTSOffset-prevCurrentPTSOffset,
			"throttle", throttle,
			"state", t,
		)
	}

	adjusted := pts + t.currentPTSOffset
	if adjusted < t.lastPTSAdjusted {
		// always move it forward
		t.logger.Infow(
			"propelling PTS forward",
			"currentTS", ts,
			"PTS", pts,
			"estimatedPTS", estimatedPTS,
			"ptsOffset", pts-estimatedPTS,
			"adjustedPTS", adjusted,
			"adjustedPTSOffset", adjusted-t.lastPTSAdjusted,
			"state", t,
		)
		adjusted = t.lastPTSAdjusted + time.Millisecond
	}

	// if past end time, return EOF
	if t.maxPTS > 0 && (adjusted > t.maxPTS) {
		t.stats.numDroppedEOF++
		return 0, io.EOF
	}

	// update previous values
	t.lastTS = ts
	t.lastTime = now
	t.lastPTS = pts
	t.lastPTSAdjusted = adjusted

	t.stats.numEmitted++
	if adjusted < 0 {
		t.stats.numNegativePTS++
	}
	return adjusted, nil
}

// onSenderReport handles pts adjustments for a track
func (t *TrackSynchronizer) onSenderReport(pkt *rtcp.SenderReport) {
	if pkt.SSRC != uint32(t.track.SSRC()) {
		return
	}

	if t.rtcpSenderReportRebaseEnabled {
		t.onSenderReportWithRebase(pkt)
	} else {
		t.onSenderReportWithoutRebase(pkt)
	}
}

func (t *TrackSynchronizer) onSenderReportWithoutRebase(pkt *rtcp.SenderReport) {
	t.Lock()
	defer t.Unlock()

	if t.startTime.IsZero() {
		return
	}

	augmented := &augmentedSenderReport{
		SenderReport: pkt,
		receivedAt:   mono.UnixNano(),
	}
	if t.lastSR != nil && ((t.lastSR.RTPTime != 0 && (pkt.RTPTime-t.lastSR.RTPTime) > (1<<31)) || pkt.RTPTime == t.lastSR.RTPTime) {
		t.logger.Infow(
			"dropping duplicate or out-of-order sender report",
			"receivedSR", wrappedAugmentedSenderReportLogger{augmented},
			"state", t,
		)
		return
	}

	t.stats.numSenderReports++

	var pts time.Duration
	if pkt.RTPTime > t.lastTS {
		pts = t.lastPTS + t.toDuration(pkt.RTPTime-t.lastTS)
	} else {
		pts = t.lastPTS - t.toDuration(t.lastTS-pkt.RTPTime)
	}
	if !t.acceptable(pts - time.Since(t.startTime)) {
		t.logger.Infow(
			"ignoring sender report with unacceptable offset",
			"receivedSR", wrappedAugmentedSenderReportLogger{augmented},
			"state", t,
			"offset", pts-time.Since(t.startTime),
		)
		return
	}

	drift := mediatransportutil.NtpTime(pkt.NTPTime).Time().Sub(t.startTime.Add(pts))
	if t.onSR != nil {
		t.onSR(drift)
	}
	if drift > cHighDriftLoggingThreshold || drift < -cHighDriftLoggingThreshold {
		t.logger.Infow(
			"high drift sender report",
			"receivedSR", wrappedAugmentedSenderReportLogger{augmented},
			"state", t,
			"drift", drift,
		)
	}

	if !t.acceptable(drift) {
		t.logger.Infow(
			"ignoring sender report with unacceptable drift",
			"receivedSR", wrappedAugmentedSenderReportLogger{augmented},
			"state", t,
			"drift", drift,
		)
		return
	}

	t.desiredPTSOffset = t.basePTSOffset + drift
	t.lastSR = augmented
}

func (t *TrackSynchronizer) onSenderReportWithRebase(pkt *rtcp.SenderReport) {
	t.Lock()
	defer t.Unlock()

	// estimate propagation, i. e. one way delay based on NTP time in the report and when it is received
	augmented := &augmentedSenderReport{
		SenderReport: pkt,
		receivedAt:   mono.UnixNano(),
	}
	estimatedPropagationDelay := time.Duration(t.updatePropagationDelay(augmented))
	// rebase the sender report NTP time to local clock
	augmented.receivedAtAdjusted = mediatransportutil.NtpTime(pkt.NTPTime).Time().Add(estimatedPropagationDelay).UnixNano()

	if t.startTime.IsZero() {
		return
	}

	if t.lastSR != nil && ((t.lastSR.RTPTime != 0 && (pkt.RTPTime-t.lastSR.RTPTime) > (1<<31)) || pkt.RTPTime == t.lastSR.RTPTime) {
		t.logger.Infow(
			"dropping duplicate or out-of-order sender report",
			"receivedSR", wrappedAugmentedSenderReportLogger{augmented},
			"state", t,
		)
		return
	}

	t.stats.numSenderReports++

	var ptsSR time.Duration
	if (pkt.RTPTime - t.lastTS) < (1 << 31) {
		ptsSR = t.lastPTS + t.toDuration(pkt.RTPTime-t.lastTS)
	} else {
		ptsSR = t.lastPTS - t.toDuration(t.lastTS-pkt.RTPTime)
	}
	if !t.acceptable(ptsSR - time.Since(t.startTime)) {
		t.logger.Infow(
			"ignoring sender report with unacceptable offset",
			"receivedSR", wrappedAugmentedSenderReportLogger{augmented},
			"state", t,
			"offset", ptsSR-time.Since(t.startTime),
		)
		return
	}

	// adjust the start time based on estimated propagation delay
	// to make subsequent PTS calculations more accurate
	adjustmentStartTimeNano := t.maybeAdjustStartTime(augmented)

	// it is possible that first sender report is late, adjust down propagation delay if that is the case
	estimatedPropagationDelay = time.Duration(t.propagationDelayEstimator.InitialAdjustment(adjustmentStartTimeNano))
	// rebase the sender report NTP time to local clock once again after initial adjustment if any
	augmented.receivedAtAdjusted = mediatransportutil.NtpTime(pkt.NTPTime).Time().Add(estimatedPropagationDelay).UnixNano()

	// drift is based on local clock
	drift := time.Unix(0, augmented.receivedAtAdjusted).Sub(t.startTime.Add(ptsSR))
	if t.onSR != nil {
		t.onSR(drift)
	}
	if drift > cHighDriftLoggingThreshold || drift < -cHighDriftLoggingThreshold {
		t.logger.Infow(
			"high drift sender report",
			"receivedSR", wrappedAugmentedSenderReportLogger{augmented},
			"state", t,
			"PTS_SR", ptsSR,
			"offset", drift,
			"ptsSRTime", t.startTime.Add(ptsSR),
			"estimatedPropagationDelay", estimatedPropagationDelay,
		)
	}

	if !t.acceptable(drift) {
		t.logger.Infow(
			"ignoring sender report with unacceptable drift",
			"receivedSR", wrappedAugmentedSenderReportLogger{augmented},
			"state", t,
			"drift", drift,
		)
		return
	}

	t.desiredPTSOffset = t.basePTSOffset + drift
	t.lastSR = augmented
}

func (t *TrackSynchronizer) updatePropagationDelay(asr *augmentedSenderReport) int64 {
	senderClockTime := mediatransportutil.NtpTime(asr.NTPTime).Time().UnixNano()
	estimatedPropagationDelay, stepChange := t.propagationDelayEstimator.Update(
		senderClockTime,
		asr.receivedAt,
	)
	if stepChange {
		t.logger.Debugw(
			"propagation delay step change",
			"receivedSR", wrappedAugmentedSenderReportLogger{asr},
			"state", t,
		)
	}

	return estimatedPropagationDelay
}

func (t *TrackSynchronizer) maybeAdjustStartTime(asr *augmentedSenderReport) int64 {
	nowNano := mono.UnixNano()
	startTimeNano := t.startTime.UnixNano()
	if time.Duration(nowNano-startTimeNano) > cStartTimeAdjustWindow || asr.receivedAtAdjusted == 0 {
		return 0
	}

	// for some time after the start, adjust time of first packet.
	// Helps improve accuracy of expected timestamp calculation.
	// Adjusting only one way, i. e. if the first sample experienced
	// abnormal delay (maybe due to pacing or maybe due to queuing
	// in some network element along the way), push back first time
	// to an earlier instance.
	timeSinceReceive := time.Duration(nowNano - asr.receivedAtAdjusted)
	nowTS := asr.RTPTime + t.toRTP(timeSinceReceive)
	samplesDiff := nowTS - t.startRTP
	if int32(samplesDiff) < 0 {
		// out-of-order, pre-start, skip
		t.logger.Debugw(
			"no adjustment due to pre-start report",
			"receivedSR", wrappedAugmentedSenderReportLogger{asr},
			"state", t,
			"nowTS", nowTS,
			"timeSinceReceive", timeSinceReceive,
			"samplesDiff", int32(samplesDiff),
		)
		return 0
	}

	samplesDuration := t.toDuration(samplesDiff)
	timeSinceStart := time.Duration(nowNano - startTimeNano)
	now := startTimeNano + timeSinceStart.Nanoseconds()
	adjustedStartTimeNano := now - samplesDuration.Nanoseconds()
	requestedAdjustment := startTimeNano - adjustedStartTimeNano

	getLoggingFields := func() []interface{} {
		return []interface{}{
			"nowTime", time.Unix(0, now),
			"before", time.Unix(0, startTimeNano),
			"after", time.Unix(0, adjustedStartTimeNano),
			"requestedAdjustment", time.Duration(requestedAdjustment),
			"nowTS", nowTS,
			"timeSinceReceive", timeSinceReceive,
			"timeSinceStart", timeSinceStart,
			"samplesDiff", samplesDiff,
			"samplesDuration", samplesDuration,
			"receivedSR", wrappedAugmentedSenderReportLogger{asr},
			"state", t,
		}
	}

	if adjustedStartTimeNano < startTimeNano {
		if requestedAdjustment > cStartTimeAdjustThreshold.Nanoseconds() {
			t.logger.Warnw(
				"adjusting start time, too big, ignoring", nil,
				getLoggingFields()...,
			)
		} else {
			applied := t.applyQuantizedStartTimeAdvance(time.Duration(requestedAdjustment))
			t.logger.Infow("adjusting start time", append(getLoggingFields(), "appliedAdjustment", applied)...)
		}
	}

	return requestedAdjustment
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
	// add a deadband of t.maxDriftAdjustment to make sure no PTS adjustment is smaller than that
	diff := t.desiredPTSOffset - t.currentPTSOffset
	if diff > -t.maxDriftAdjustment && diff < t.maxDriftAdjustment {
		return false
	}

	return adjustmentEnabled && (t.currentPTSOffset != t.desiredPTSOffset)
}

func (t *TrackSynchronizer) isPacketTooOld(packetTime time.Time) bool {
	return t.oldPacketThreshold != 0 && mono.Now().Sub(packetTime) > t.oldPacketThreshold
}

// avoid applying small changes to start time as it will cause subsequent PTSes
// to have micro jumps potentially causing audible distortion,
// the bet is more infrequent larger jumps  is better than more frequent smaller jumps
func (t *TrackSynchronizer) applyQuantizedStartTimeAdvance(deltaTotal time.Duration) time.Duration {
	// include any prior residual
	deltaTotal += t.startTimeAdjustResidual

	quanta := deltaTotal / t.maxDriftAdjustment
	residual := deltaTotal % t.maxDriftAdjustment

	if quanta > 0 {
		applied := quanta * t.maxDriftAdjustment
		t.startTime = t.startTime.Add(-applied)
		t.totalStartTimeAdjustment += applied
		t.startTimeAdjustResidual = residual
		return applied
	}

	t.startTimeAdjustResidual = deltaTotal
	return 0
}

func (t *TrackSynchronizer) updateGapHistogram(gap uint16) {
	if gap < 2 {
		return
	}

	missing := gap - 1
	if int(missing) > len(t.stats.gapHistogram) {
		t.stats.gapHistogram[len(t.stats.gapHistogram)-1]++
	} else {
		t.stats.gapHistogram[missing-1]++
	}

	t.stats.largestGap = max(missing, t.stats.largestGap)
}

func (t *TrackSynchronizer) MarshalLogObject(e zapcore.ObjectEncoder) error {
	if t == nil {
		return nil
	}

	e.AddTime("startTime", t.startTime)
	e.AddUint32("startRTP", t.startRTP)
	e.AddTime("firstTime", t.firstTime)
	e.AddUint32("lastTS", t.lastTS)
	e.AddTime("lastTime", t.lastTime)
	e.AddDuration("lastPTS", t.lastPTS)
	e.AddDuration("lastPTSAdjusted", t.lastPTSAdjusted)
	e.AddUint32("lastTSOldDropped", t.lastTSOldDropped)
	e.AddDuration("maxPTS", t.maxPTS)
	e.AddDuration("currentPTSOffset", t.currentPTSOffset)
	e.AddDuration("desiredPTSOffset", t.desiredPTSOffset)
	e.AddDuration("basePTSOffset", t.basePTSOffset)
	e.AddDuration("totalPTSAdjustmentPositive", t.totalPTSAdjustmentPositive)
	e.AddDuration("totalPTSAdjustmentNegative", t.totalPTSAdjustmentNegative)
	e.AddUint16("lastSN", t.lastSN)
	e.AddObject("lastSR", wrappedAugmentedSenderReportLogger{t.lastSR})
	e.AddTime("nextPTSAdjustmentAt", t.nextPTSAdjustmentAt)
	e.AddObject("propagationDelayEstimator", t.propagationDelayEstimator)
	e.AddDuration("totalStartTimeAdjustment", t.totalStartTimeAdjustment)
	e.AddDuration("startTimeAdjustResidual", t.startTimeAdjustResidual)
	e.AddObject("stats", t.stats)
	return nil
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

// -----------------------------

type augmentedSenderReport struct {
	*rtcp.SenderReport
	receivedAt         int64
	receivedAtAdjusted int64
}

// -----------------------------

type wrappedAugmentedSenderReportLogger struct {
	*augmentedSenderReport
}

func (w wrappedAugmentedSenderReportLogger) MarshalLogObject(e zapcore.ObjectEncoder) error {
	asr := w.augmentedSenderReport
	if asr == nil {
		return nil
	}

	e.AddUint32("SSRC", asr.SSRC)
	e.AddUint32("RTPTime", asr.RTPTime)
	e.AddTime("NTPTime", mediatransportutil.NtpTime(asr.NTPTime).Time())
	e.AddUint32("PacketCount", asr.PacketCount)
	e.AddUint32("OctetCount", asr.OctetCount)
	e.AddTime("receivedAt", time.Unix(0, asr.receivedAt))
	e.AddTime("receivedAtAdjusted", time.Unix(0, asr.receivedAtAdjusted))
	return nil
}

// -----------------------------

type stats struct {
	// packet stats
	numEmitted           uint32
	numDroppedOld        uint32
	numDroppedOutOfOrder uint32
	numDroppedEOF        uint32

	numNegativePTS uint32

	gapHistogram [cGapHistogramNumBins]uint32
	largestGap   uint16

	// sender report stats
	numSenderReports uint32
}

func (s stats) MarshalLogObject(e zapcore.ObjectEncoder) error {
	e.AddUint32("numEmitted", s.numEmitted)
	e.AddUint32("numDroppedOld", s.numDroppedOld)
	e.AddUint32("numDroppedOutOfOrder", s.numDroppedOutOfOrder)
	e.AddUint32("numDroppedEOF", s.numDroppedEOF)

	e.AddUint32("numNegativePTS", s.numNegativePTS)

	hasLoss := false
	first := true
	str := "["
	for burst, count := range s.gapHistogram {
		if count == 0 {
			continue
		}

		hasLoss = true

		if !first {
			str += ", "
		}
		first = false
		str += fmt.Sprintf("%d:%d", burst+1, count)
	}
	str += "]"
	if hasLoss {
		e.AddString("gapHistogram", str)
	}

	e.AddUint16("largestGap", s.largestGap)

	e.AddUint32("numSenderReports", s.numSenderReports)
	return nil
}
