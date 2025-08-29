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

	"github.com/livekit/mediatransportutil"
	"github.com/livekit/protocol/logger"
)

const (
	maxAdjustment = time.Millisecond * 5
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
	maxTsDiff time.Duration // maximum acceptable difference between RTP packets

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
}

func newTrackSynchronizer(s *Synchronizer, track TrackRemote) *TrackSynchronizer {
	t := &TrackSynchronizer{
		sync:         s,
		track:        track,
		logger:       logger.GetLogger().WithValues("trackID", track.ID(), "codec", track.Codec().MimeType),
		rtpConverter: newRTPConverter(int64(track.Codec().ClockRate)),
		maxTsDiff:    s.config.MaxTsDiff,
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
}

// GetPTS will reset sequence numbers and/or offsets if necessary
// Packets are expected to be in order
func (t *TrackSynchronizer) GetPTS(pkt *rtp.Packet) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	if t.startTime.IsZero() {
		t.startTime = time.Now()
	}

	ts := pkt.Timestamp
	if ts == t.lastTS {
		return t.lastPTSAdjusted, nil
	}

	pts := t.lastPTS + t.toDuration(ts-t.lastTS)
	estimatedPTS := time.Since(t.startTime)
	if pts < t.lastPTS || !t.acceptable(pts-estimatedPTS) {
		pts = estimatedPTS
		t.startRTP = ts - t.toRTP(pts)
	}

	if t.currentPTSOffset > t.desiredPTSOffset {
		t.currentPTSOffset = max(t.currentPTSOffset-maxAdjustment, t.desiredPTSOffset)
	} else if t.currentPTSOffset < t.desiredPTSOffset {
		t.currentPTSOffset = min(t.currentPTSOffset+maxAdjustment, t.desiredPTSOffset)
	}
	adjusted := pts + t.currentPTSOffset

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

	if pkt.RTPTime == t.lastSR || t.startTime.IsZero() {
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

func (t *TrackSynchronizer) acceptable(d time.Duration) bool {
	return d > -t.maxTsDiff && d < t.maxTsDiff
}

type rtpConverter struct {
	ts  uint64
	rtp uint64
}

func newRTPConverter(clockRate int64) *rtpConverter {
	ts := int64(time.Second)
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
	return uint32(uint64(duration.Nanoseconds()) * c.rtp / c.ts)
}
