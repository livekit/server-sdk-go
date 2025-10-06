// Copyright 2025 LiveKit, Inc.
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
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/mono"
)

// ---- test fakes & helpers ----

type fakeTrack struct {
	id   string
	rate uint32
	kind webrtc.RTPCodecType
}

func (f fakeTrack) ID() string { return f.id }
func (f fakeTrack) Codec() webrtc.RTPCodecParameters {
	return webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{ClockRate: f.rate}}
}
func (f fakeTrack) Kind() webrtc.RTPCodecType { return f.kind }
func (f fakeTrack) SSRC() webrtc.SSRC         { return 1234 }

func newTSForTests(tc *testing.T, clockRate uint32, kind webrtc.RTPCodecType) *TrackSynchronizer {
	t := &TrackSynchronizer{
		sync:               nil, // construct directly to avoid depending on Synchronizer
		track:              fakeTrack{id: "t", rate: clockRate, kind: kind},
		logger:             logger.NewTestLogger(tc),
		rtpConverter:       newRTPConverter(int64(clockRate)),
		maxTsDiff:          200 * time.Millisecond,
		maxDriftAdjustment: 5 * time.Millisecond,
	}
	// set a stable startTime well in the past to make time.Since(startTime) > 0
	t.startTime = time.Now().Add(-150 * time.Millisecond)
	// pick an arbitrary RTP base
	t.startRTP = 1000
	return t
}

// ---- tests ----

func TestApplyQuantizedStartTimeAdvance_ExactQuanta(t *testing.T) {
	ts := newTSForTests(t, 48000, webrtc.RTPCodecTypeAudio)
	base := ts.startTime

	// 25ms delta with 5ms step -> apply 25ms, residual 0
	applied := ts.applyQuantizedStartTimeAdvance(25 * time.Millisecond)
	require.Equal(t, 25*time.Millisecond, applied)
	require.Equal(t, 25*time.Millisecond, base.Sub(ts.startTime))
	require.Equal(t, time.Duration(0), ts.startTimeAdjustResidual)
	require.Equal(t, 25*time.Millisecond, ts.totalStartTimeAdjustment)
}

func TestApplyQuantizedStartTimeAdvance_ResidualCarryAcrossCalls(t *testing.T) {
	ts := newTSForTests(t, 48000, webrtc.RTPCodecTypeAudio)
	base := ts.startTime

	// First call: 3ms (<5ms step) -> apply 0, residual=3ms
	applied1 := ts.applyQuantizedStartTimeAdvance(3 * time.Millisecond)
	require.Equal(t, time.Duration(0), applied1)
	require.Equal(t, 3*time.Millisecond, ts.startTimeAdjustResidual)
	require.Equal(t, ts.startTime, base)
	require.Equal(t, 3*time.Millisecond, ts.startTimeAdjustResidual)
}

func TestApplyQuantizedStartTimeAdvance_NoOpForZero(t *testing.T) {
	ts := newTSForTests(t, 48000, webrtc.RTPCodecTypeAudio)
	base := ts.startTime

	applied := ts.applyQuantizedStartTimeAdvance(0)
	require.Equal(t, time.Duration(0), applied)
	require.Equal(t, ts.startTime, base)
	require.Equal(t, time.Duration(0), ts.startTimeAdjustResidual)
}

func TestGetPTSWithoutRebase_Increasing(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeAudio)

	// Simulate accepting two frames in order: 20ms and then 20ms later
	// Convert 20ms -> RTP ticks
	rtp20ms := ts.rtpConverter.toRTP(20 * time.Millisecond)

	now := time.Now()
	// First packet initializes lastTS path
	ts.lastTS = 0
	ts.lastPTS = 0

	adj1, err := ts.getPTSWithoutRebase(jitter.ExtPacket{
		Packet:     &rtp.Packet{Header: rtp.Header{Timestamp: ts.startRTP + rtp20ms}},
		ReceivedAt: now,
	})
	require.NoError(t, err)

	adj2, err := ts.getPTSWithoutRebase(jitter.ExtPacket{
		Packet:     &rtp.Packet{Header: rtp.Header{Timestamp: ts.startRTP + 2*rtp20ms}},
		ReceivedAt: now,
	})
	require.NoError(t, err)

	require.Greater(t, adj2, adj1)
}

func TestGetPTSWithRebase_PropelsForward(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeAudio)
	ts.rtcpSenderReportRebaseEnabled = true

	ts.maxTsDiff = 30 * time.Millisecond

	// 1) Seed: make adjusted ~500ms on the first packet.
	ts.startTime = mono.Now().Add(-500 * time.Millisecond)
	ts.currentPTSOffset = 0
	ts.lastPTS = 0
	ts.startRTP = 100000
	ts.lastTS = ts.startRTP

	rtp500ms := ts.rtpConverter.toRTP(500 * time.Millisecond)
	rtp10ms := ts.rtpConverter.toRTP(10 * time.Millisecond)

	// First packet (~500ms)
	ts1 := ts.startRTP + rtp500ms
	adj1, err := ts.getPTSWithRebase(jitter.ExtPacket{
		Packet:     &rtp.Packet{Header: rtp.Header{Timestamp: ts1}},
		ReceivedAt: mono.Now(),
	})
	require.NoError(t, err)
	require.InDelta(t, 500*time.Millisecond, adj1, float64(20*time.Millisecond))

	// 2) Simulate startTime shift LATER (closer to now) so next estimatedPTS is tiny (~5–10ms)
	ts.startTime = mono.Now().Add(-5 * time.Millisecond)

	// Second packet: +10ms RTP so ts != lastTS. After correction, adjusted will be tiny and < lastPTSAdjusted.
	ts2 := ts1 + rtp10ms
	prev := ts.lastPTSAdjusted      // ~500ms from first call
	want := prev + time.Millisecond // propel to ~501ms

	adj2, err := ts.getPTSWithRebase(jitter.ExtPacket{
		Packet:     &rtp.Packet{Header: rtp.Header{Timestamp: ts2}},
		ReceivedAt: mono.Now(),
	})
	require.NoError(t, err)
	require.Equal(t, want, adj2)
}

func TestShouldAdjustPTS_Deadband_Suppresses(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeVideo) // video avoids audio gating path
	ts.maxDriftAdjustment = 5 * time.Millisecond
	ts.currentPTSOffset = 100 * time.Millisecond

	// within dead-band: +4ms
	ts.desiredPTSOffset = 104 * time.Millisecond

	// ensure throttle window has elapsed
	ts.nextPTSAdjustmentAt = mono.Now().Add(-time.Second)

	require.False(t, ts.shouldAdjustPTS(), "delta < step should suppress adjustment")
}

func TestShouldAdjustPTS_Deadband_BoundaryAdjusts(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeVideo)
	ts.maxDriftAdjustment = 5 * time.Millisecond
	ts.currentPTSOffset = 100 * time.Millisecond

	// exactly at boundary: +5ms
	ts.desiredPTSOffset = 105 * time.Millisecond
	ts.nextPTSAdjustmentAt = mono.Now().Add(-time.Second)

	require.True(t, ts.shouldAdjustPTS(), "delta == step should allow adjustment")
}

func TestShouldAdjustPTS_Deadband_AboveAdjusts(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeVideo)
	ts.maxDriftAdjustment = 5 * time.Millisecond
	ts.currentPTSOffset = 100 * time.Millisecond

	// above dead-band: +12ms
	ts.desiredPTSOffset = 112 * time.Millisecond
	ts.nextPTSAdjustmentAt = mono.Now().Add(-time.Second)

	require.True(t, ts.shouldAdjustPTS(), "delta > step should allow adjustment")
}

func TestPrimeForStartWithStartGate(t *testing.T) {
	clock := uint32(90000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeVideo)
	ts.startGate = newStartGate(clock, webrtc.RTPCodecTypeVideo, ts.logger)
	ts.sync = NewSynchronizerWithOptions()

	stepDur := 20 * time.Millisecond
	step := ts.rtpConverter.toRTP(stepDur)
	baseTS := ts.startRTP
	base := time.Now()

	for i := 0; i < 5; i++ {
		timestamp := baseTS + uint32(i+1)*step
		pkt := jitter.ExtPacket{
			Packet:     &rtp.Packet{Header: rtp.Header{Timestamp: timestamp}, Payload: []byte{0x01}},
			ReceivedAt: base.Add(time.Duration(i+1) * stepDur),
		}
		ready, dropped, done := ts.PrimeForStart(pkt)
		require.False(t, done)
		require.Zero(t, dropped)
		require.Nil(t, ready)
	}

	finalPkt := jitter.ExtPacket{
		Packet:     &rtp.Packet{Header: rtp.Header{Timestamp: baseTS + uint32(6*step)}, Payload: []byte{0x02}},
		ReceivedAt: base.Add(6 * stepDur),
	}
	ready, dropped, done := ts.PrimeForStart(finalPkt)
	require.True(t, done, "gate should be done after final packet")
	require.Zero(t, dropped, "no packets should be dropped")
	require.NotEmpty(t, ready, "ready should have at least the final packet")
	require.True(t, ts.initialized, "track should be initialized")
}

func TestPrimeForStartWithoutStartGate(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeAudio)
	ts.startGate = nil
	ts.sync = NewSynchronizerWithOptions()

	pkt := jitter.ExtPacket{
		Packet:     &rtp.Packet{Header: rtp.Header{Timestamp: ts.startRTP + 1234}, Payload: []byte{0x01}},
		ReceivedAt: time.Now(),
	}

	ready, dropped, done := ts.PrimeForStart(pkt)
	require.True(t, done, "gate should be done after packet")
	require.Zero(t, dropped, "no packets should be dropped")
	require.Len(t, ready, 1, "ready should have the packet")
	require.True(t, ts.initialized, "track should be initialized")
}

func TestShouldAdjustPTS_Deadband_NegativeDelta_Suppresses(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeVideo)
	ts.maxDriftAdjustment = 5 * time.Millisecond
	ts.currentPTSOffset = 100 * time.Millisecond

	// within dead-band on negative side: -4ms
	ts.desiredPTSOffset = 96 * time.Millisecond
	ts.nextPTSAdjustmentAt = mono.Now().Add(-time.Second)

	require.False(t, ts.shouldAdjustPTS(), "negative delta with |delta| < step should suppress adjustment")
}

func TestShouldAdjustPTS_Deadband_NegativeDelta_AboveAdjusts(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeVideo)
	ts.maxDriftAdjustment = 5 * time.Millisecond
	ts.currentPTSOffset = 100 * time.Millisecond

	// beyond dead-band on negative side: -6ms
	ts.desiredPTSOffset = 94 * time.Millisecond
	ts.nextPTSAdjustmentAt = mono.Now().Add(-time.Second)

	require.True(t, ts.shouldAdjustPTS(), "negative delta with |delta| > step should allow adjustment")
}

func TestShouldAdjustPTS_AudioGating_DisabledBlocks(t *testing.T) {
	clock := uint32(48000)
	ts := newTSForTests(t, clock, webrtc.RTPCodecTypeAudio)

	// Force the path where audio gating can block adjustments:
	ts.rtcpSenderReportRebaseEnabled = false
	ts.audioPTSAdjustmentsDisabled = true

	ts.maxDriftAdjustment = 5 * time.Millisecond
	ts.currentPTSOffset = 100 * time.Millisecond
	ts.desiredPTSOffset = 140 * time.Millisecond // large delta
	ts.nextPTSAdjustmentAt = mono.Now().Add(-time.Second)

	require.False(t, ts.shouldAdjustPTS(), "audio gating should block adjustment when disabled")
}
