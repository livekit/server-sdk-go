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
	maxAdjustment = time.Millisecond * 5

	cStartTimeAdjustWindow    = 2 * time.Minute
	cStartTimeAdjustThreshold = 15 * 1e9
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
	maxTsDiff                   time.Duration // maximum acceptable difference between RTP packets
	audioPTSAdjustmentsDisabled bool          // disable audio packets PTS adjustments on SRs

	// timing info
	startTime       time.Time     // time first packet was pushed
	startRTP        uint32        // RTP timestamp of PTS 0
	lastTS          uint32        // previous RTP timestamp
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

	propagationDelayEstimator *OWDEstimator
	totalStartTimeAdjustment  time.Duration
}

func newTrackSynchronizer(s *Synchronizer, track TrackRemote) *TrackSynchronizer {
	t := &TrackSynchronizer{
		sync:                        s,
		track:                       track,
		logger:                      logger.GetLogger().WithValues("trackID", track.ID(), "codec", track.Codec().MimeType),
		rtpConverter:                newRTPConverter(int64(track.Codec().ClockRate)),
		maxTsDiff:                   s.config.MaxTsDiff,
		audioPTSAdjustmentsDisabled: s.config.AudioPTSAdjustmentDisabled,
		propagationDelayEstimator:   NewOWDEstimator(OWDEstimatorParamsDefault),
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
	now := time.Now()
	startedAt := t.sync.getOrSetStartedAt(now.UnixNano())

	t.Lock()
	defer t.Unlock()

	t.currentPTSOffset = time.Duration(now.UnixNano() - startedAt)
	t.desiredPTSOffset = t.currentPTSOffset
	t.basePTSOffset = t.desiredPTSOffset

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

	pts := t.lastPTS + t.toDuration(ts-t.lastTS)
	estimatedPTS := time.Since(t.startTime)
	if pts < t.lastPTS || !t.acceptable(pts-estimatedPTS) {
		t.logger.Infow(
			"correcting PTS",
			"currentTS", ts,
			"lastTS", t.lastTS,
			"PTS", pts,
			"lastPTS", t.lastPTS,
			"estimatedPTS", estimatedPTS,
			"offset", pts-estimatedPTS,
			"startRTP", t.startRTP,
			"propagationDelay", t.propagationDelayEstimator,
			"totalStartTimeAdjustment", t.totalStartTimeAdjustment,
		)
		pts = estimatedPTS
	}

	if t.shouldAdjustPTS() {
		if t.currentPTSOffset > t.desiredPTSOffset {
			t.currentPTSOffset = max(t.currentPTSOffset-maxAdjustment, t.desiredPTSOffset)
		} else if t.currentPTSOffset < t.desiredPTSOffset {
			t.currentPTSOffset = min(t.currentPTSOffset+maxAdjustment, t.desiredPTSOffset)
		}
	}

	adjusted := pts + t.currentPTSOffset
	if adjusted < t.lastPTSAdjusted {
		// always move it forward
		t.logger.Infow(
			"propelling PTS forward",
			"currentTS", ts,
			"lastTS", t.lastTS,
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
		)
		adjusted = t.lastPTSAdjusted + time.Millisecond
	}

	// if past end time, return EOF
	if t.maxPTS > 0 && (adjusted > t.maxPTS) {
		return 0, io.EOF
	}

	// update previous values
	t.lastTS = ts
	t.lastPTS = pts
	t.lastPTSAdjusted = adjusted

	return adjusted, nil
}

// onSenderReport handles pts adjustments for a track
func (t *TrackSynchronizer) onSenderReport(pkt *rtcp.SenderReport) {
	t.Lock()
	defer t.Unlock()

	// estimate propagation, i. e. one way delay based on NTP time in the report and when it is received
	receivedAt := mono.UnixNano()
	estimatedPropagationDelay := t.updatePropagationDelay(pkt, receivedAt)

	if (t.lastSR != 0 && (pkt.RTPTime-t.lastSR) > (1<<31)) || t.startTime.IsZero() {
		return
	}

	var pts time.Duration
	if (pkt.RTPTime - t.lastTS) < (1 << 31) {
		pts = t.lastPTS + t.toDuration(pkt.RTPTime-t.lastTS)
	} else {
		pts = t.lastPTS - t.toDuration(t.lastTS-pkt.RTPTime)
	}
	if !t.acceptable(pts - time.Since(t.startTime)) {
		return
	}

	// rebase the sender report NTP time to local clock
	rebasedSenderTime := mediatransportutil.NtpTime(pkt.NTPTime).Time().Add(estimatedPropagationDelay)

	// adjust the start time based on estimated propagation delay
	// to make subsequent PTS calculations more accurate
	t.maybeAdjustStartTime(pkt, rebasedSenderTime.UnixNano())

	// offset is based on local clock
	offset := rebasedSenderTime.Sub(t.startTime.Add(pts))
	if t.onSR != nil {
		t.onSR(offset)
	}
	if offset > 500*time.Millisecond {
		t.logger.Infow(
			"high offset",
			"lastTS", t.lastTS,
			"PTS", pts,
			"lastPTS", t.lastPTS,
			"startRTP", t.startRTP,
			"propagationDelay", t.propagationDelayEstimator,
			"totalStartTimeAdjustment", t.totalStartTimeAdjustment,
			"offset", offset,
			"rebasedSenderTime", rebasedSenderTime,
			"startTime", t.startTime,
			"ptsTime", t.startTime.Add(pts),
		)
	}

	if !t.acceptable(offset) {
		return
	}

	t.desiredPTSOffset = t.basePTSOffset + offset
	t.lastSR = pkt.RTPTime
}

func (t *TrackSynchronizer) updatePropagationDelay(sr *rtcp.SenderReport, receivedAt int64) time.Duration {
	senderClockTime := mediatransportutil.NtpTime(sr.NTPTime).Time().UnixNano()
	estimatedPropagationDelay, stepChange := t.propagationDelayEstimator.Update(
		senderClockTime,
		receivedAt,
	)
	if stepChange {
		t.logger.Debugw(
			"propagation delay step change",
			"propagationDelayEstimator", t.propagationDelayEstimator,
		)
	}

	return time.Duration(estimatedPropagationDelay)
}

func (t *TrackSynchronizer) maybeAdjustStartTime(sr *rtcp.SenderReport, rebasedReceivedAt int64) {
	nowNano := mono.UnixNano()
	startTimeNano := t.startTime.UnixNano()
	if time.Duration(nowNano-startTimeNano) > cStartTimeAdjustWindow {
		return
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
		return
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
		if startTimeNano-adjustedStartTimeNano > cStartTimeAdjustThreshold {
			t.logger.Warnw(
				"adjusting start time, too big, ignoring", nil,
				getLoggingFields()...,
			)
		} else {
			t.logger.Debugw("adjusting first packet time", getLoggingFields()...)
			t.totalStartTimeAdjustment += time.Duration(startTimeNano - adjustedStartTimeNano)
			t.startTime = time.Unix(0, adjustedStartTimeNano)
		}
	}
	return
}

func (t *TrackSynchronizer) acceptable(d time.Duration) bool {
	return d > -t.maxTsDiff && d < t.maxTsDiff
}

func (t *TrackSynchronizer) shouldAdjustPTS() bool {
	adjustmentEnabled := true
	if t.track.Kind() == webrtc.RTPCodecTypeAudio {
		adjustmentEnabled = !t.audioPTSAdjustmentsDisabled
	}
	return adjustmentEnabled && (t.currentPTSOffset != t.desiredPTSOffset)
}

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
