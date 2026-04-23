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
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk/jitter"
)

// --- Test helpers ---

type mockTrackRemote struct {
	id    string
	codec webrtc.RTPCodecParameters
	kind  webrtc.RTPCodecType
	ssrc  webrtc.SSRC
}

func (m *mockTrackRemote) ID() string                       { return m.id }
func (m *mockTrackRemote) Codec() webrtc.RTPCodecParameters { return m.codec }
func (m *mockTrackRemote) Kind() webrtc.RTPCodecType        { return m.kind }
func (m *mockTrackRemote) SSRC() webrtc.SSRC                { return m.ssrc }

func newMockAudioTrack(id string, ssrc uint32) *mockTrackRemote {
	return &mockTrackRemote{
		id:    id,
		codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{ClockRate: 48000}},
		kind:  webrtc.RTPCodecTypeAudio,
		ssrc:  webrtc.SSRC(ssrc),
	}
}

func newMockVideoTrack(id string, ssrc uint32) *mockTrackRemote {
	return &mockTrackRemote{
		id:    id,
		codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{ClockRate: 90000}},
		kind:  webrtc.RTPCodecTypeVideo,
		ssrc:  webrtc.SSRC(ssrc),
	}
}

func makeExtPacket(ts uint32, sn uint16, receivedAt time.Time) jitter.ExtPacket {
	return jitter.ExtPacket{
		ReceivedAt: receivedAt,
		Packet:     &rtp.Packet{Header: rtp.Header{Timestamp: ts, SequenceNumber: sn}},
	}
}

// --- Tests ---

func TestSyncEngine_ImplementsSyncInterface(t *testing.T) {
	// Compile-time check that SyncEngine implements Sync.
	var _ Sync = (*SyncEngine)(nil)
}

func TestSyncEngine_FallbackToWallClockBeforeSRs(t *testing.T) {
	engine := NewSyncEngine()

	track := newMockAudioTrack("audio-1", 1000)
	ts := engine.AddTrack(track, "alice")

	now := time.Now()

	// Prime the track with the first packet.
	pkt0 := makeExtPacket(0, 0, now)
	_, _, done := ts.PrimeForStart(pkt0)
	require.True(t, done, "without start gate, track should be ready immediately")

	// Get PTS for first packet (same as prime packet).
	pts0, err := ts.GetPTS(pkt0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, int64(pts0), int64(0), "first PTS should be >= 0")

	// Second packet 100ms later.
	pkt1 := makeExtPacket(4800, 1, now.Add(100*time.Millisecond))
	pts1, err := ts.GetPTS(pkt1)
	require.NoError(t, err)
	require.Greater(t, int64(pts1), int64(0), "second packet PTS should be > 0")
	require.Greater(t, int64(pts1), int64(pts0), "PTS should advance")
}

func TestSyncEngine_TransitionsToNTPAfterSRs(t *testing.T) {
	engine := NewSyncEngine()

	track := newMockAudioTrack("audio-1", 1000)
	ts := engine.AddTrack(track, "alice")

	now := time.Now()

	// Prime and get initial wall-clock PTS.
	pkt0 := makeExtPacket(0, 0, now)
	ts.PrimeForStart(pkt0)
	pts0, err := ts.GetPTS(pkt0)
	require.NoError(t, err)

	// Get a wall-clock PTS at 500ms.
	pkt1 := makeExtPacket(24000, 1, now.Add(500*time.Millisecond))
	pts1, err := ts.GetPTS(pkt1)
	require.NoError(t, err)
	require.Greater(t, int64(pts1), int64(pts0))

	// Feed 3 sender reports to make NTP estimator ready.
	for i := 0; i < 3; i++ {
		srTime := now.Add(time.Duration(i) * time.Second)
		rtpTS := uint32(i) * 48000
		ntpTime := ntpToUint64(srTime)
		sr := makeSenderReport(1000, ntpTime, rtpTS)
		engine.OnRTCP(sr)
	}

	// Get PTS after NTP transition - should still be valid and advancing.
	pkt2 := makeExtPacket(48000, 2, now.Add(time.Second))
	pts2, err := ts.GetPTS(pkt2)
	require.NoError(t, err)
	require.Greater(t, int64(pts2), int64(pts1), "PTS should continue to advance after NTP transition")
}

func TestSyncEngine_MonotonicPTS(t *testing.T) {
	engine := NewSyncEngine()

	track := newMockAudioTrack("audio-1", 1000)
	ts := engine.AddTrack(track, "alice")

	now := time.Now()

	// Prime with first packet.
	pkt0 := makeExtPacket(0, 0, now)
	ts.PrimeForStart(pkt0)

	var lastPTS time.Duration
	for i := 0; i < 100; i++ {
		recvAt := now.Add(time.Duration(i) * 20 * time.Millisecond)
		rtpTS := uint32(i) * 960 // 20ms at 48kHz
		pkt := makeExtPacket(rtpTS, uint16(i), recvAt)
		pts, err := ts.GetPTS(pkt)
		require.NoError(t, err)
		require.GreaterOrEqual(t, int64(pts), int64(lastPTS),
			"PTS must be monotonically non-decreasing: packet %d got %v, last was %v", i, pts, lastPTS)
		lastPTS = pts
	}
}

func TestSyncEngine_EndDrain(t *testing.T) {
	engine := NewSyncEngine()

	track := newMockAudioTrack("audio-1", 1000)
	ts := engine.AddTrack(track, "alice")

	now := time.Now()

	// Prime and push some packets.
	pkt0 := makeExtPacket(0, 0, now)
	ts.PrimeForStart(pkt0)
	ts.GetPTS(pkt0)

	for i := 1; i <= 10; i++ {
		recvAt := now.Add(time.Duration(i) * 20 * time.Millisecond)
		rtpTS := uint32(i) * 960
		pkt := makeExtPacket(rtpTS, uint16(i), recvAt)
		ts.GetPTS(pkt)
	}

	require.Equal(t, int64(0), engine.GetEndedAt(), "endedAt should be 0 before End()")

	engine.End()

	require.Greater(t, engine.GetEndedAt(), int64(0), "endedAt should be > 0 after End()")
	require.Greater(t, engine.GetStartedAt(), int64(0), "startedAt should be > 0")
}

// makeSenderReport creates an rtcp.SenderReport with the given fields.
func makeSenderReport(ssrc uint32, ntpTime uint64, rtpTime uint32) *rtcp.SenderReport {
	return &rtcp.SenderReport{
		SSRC:    ssrc,
		NTPTime: ntpTime,
		RTPTime: rtpTime,
	}
}
