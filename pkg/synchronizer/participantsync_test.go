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

	"github.com/stretchr/testify/require"
)

// readyEstimator creates an NtpEstimator pre-loaded with `count` sender reports
// so that it is ready for use. The SR samples are spaced 5 seconds apart in both
// NTP and RTP time.
func readyEstimator(clockRate uint32, baseNtp time.Time, baseRtp uint32, count int) *NtpEstimator {
	e := NewNtpEstimator(clockRate)
	for i := 0; i < count; i++ {
		ntpTime := baseNtp.Add(time.Duration(i) * 5 * time.Second)
		rtpTS := baseRtp + uint32(i)*uint32(clockRate)*5
		e.OnSenderReport(ntpToUint64(ntpTime), rtpTS, ntpTime.Add(30*time.Millisecond))
	}
	return e
}

func TestParticipantSync_NoAdjustmentBeforeReady(t *testing.T) {
	ps := NewParticipantSync()

	// Register a non-ready estimator (only 1 SR, need >= 2).
	e := NewNtpEstimator(90000)
	baseNtp := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	e.OnSenderReport(ntpToUint64(baseNtp), 0, baseNtp.Add(30*time.Millisecond))

	require.False(t, e.IsReady(), "estimator should not be ready with 1 SR")

	ps.SetTrackEstimator("audio-1", MediaTypeAudio, e)
	ps.SetTrackEstimator("video-1", MediaTypeVideo, NewNtpEstimator(90000))

	ps.OnSenderReport("audio-1")
	ps.updateAdjustments(time.Second)

	require.Equal(t, time.Duration(0), ps.GetAdjustment("audio-1"),
		"audio adjustment should be zero before estimator is ready")
	require.Equal(t, time.Duration(0), ps.GetAdjustment("video-1"),
		"video adjustment should be zero before estimator is ready")
}

func TestParticipantSync_DeadbandSuppressesSmallOffset(t *testing.T) {
	ps := NewParticipantSync()

	baseNtp := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Audio and video with the same NTP base => perfectly aligned.
	audioEst := readyEstimator(48000, baseNtp, 0, 5)
	videoEst := readyEstimator(90000, baseNtp, 0, 5)

	ps.SetTrackEstimator("audio-1", MediaTypeAudio, audioEst)
	ps.SetTrackEstimator("video-1", MediaTypeVideo, videoEst)

	ps.OnSenderReport("audio-1")
	ps.OnSenderReport("video-1")

	// Drive enough updates to let any adjustment converge.
	for i := 0; i < 100; i++ {
		ps.updateAdjustments(time.Duration(i) * 100 * time.Millisecond)
	}

	require.Equal(t, time.Duration(0), ps.GetAdjustment("audio-1"),
		"audio adjustment should be zero when perfectly aligned")
	require.Equal(t, time.Duration(0), ps.GetAdjustment("video-1"),
		"video adjustment should be zero when perfectly aligned")
}

func TestParticipantSync_VideoAdjustsToMatchAudio(t *testing.T) {
	ps := NewParticipantSync()

	baseNtp := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Audio starts at baseNtp, video starts 100ms later in NTP.
	// This means video's NTP clock is 100ms ahead of audio's.
	// The offset (video NTP - audio NTP) = +100ms.
	// Since audio is reference, video must be delayed by -100ms.
	audioEst := readyEstimator(48000, baseNtp, 0, 5)
	videoEst := readyEstimator(90000, baseNtp.Add(100*time.Millisecond), 0, 5)

	ps.SetTrackEstimator("audio-1", MediaTypeAudio, audioEst)
	ps.SetTrackEstimator("video-1", MediaTypeVideo, videoEst)

	ps.OnSenderReport("audio-1")
	ps.OnSenderReport("video-1")

	// Drive enough time to converge: 100ms offset / 5ms per second = 20 seconds.
	// Use more to ensure full convergence.
	for i := 0; i <= 300; i++ {
		ps.updateAdjustments(time.Duration(i) * 100 * time.Millisecond)
	}

	audioAdj := ps.GetAdjustment("audio-1")
	videoAdj := ps.GetAdjustment("video-1")

	// Audio is the reference, should stay near zero.
	require.InDelta(t, 0, float64(audioAdj), float64(time.Millisecond),
		"audio adjustment should be near zero, got %v", audioAdj)

	// Video should get approximately -100ms adjustment.
	require.InDelta(t, float64(-100*time.Millisecond), float64(videoAdj), float64(10*time.Millisecond),
		"video adjustment should be ~-100ms, got %v", videoAdj)
}

func TestParticipantSync_SlewRateIsTimeBased(t *testing.T) {
	ps := NewParticipantSync()

	baseNtp := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// 200ms offset: video NTP is 200ms ahead of audio.
	audioEst := readyEstimator(48000, baseNtp, 0, 5)
	videoEst := readyEstimator(90000, baseNtp.Add(200*time.Millisecond), 0, 5)

	ps.SetTrackEstimator("audio-1", MediaTypeAudio, audioEst)
	ps.SetTrackEstimator("video-1", MediaTypeVideo, videoEst)

	ps.OnSenderReport("audio-1")
	ps.OnSenderReport("video-1")

	// Drive exactly 1 second of session time (slew rate = 5ms/s, so max adjustment = 5ms after 1s).
	// Start from 0 to 1 second.
	ps.updateAdjustments(0)
	ps.updateAdjustments(time.Second)

	videoAdj := ps.GetAdjustment("video-1")

	// After 1 second of real time at 5ms/s slew, adjustment magnitude should be <= ~5ms.
	// We add a small tolerance (1ms).
	require.LessOrEqual(t, -videoAdj, 6*time.Millisecond,
		"after 1s, video adjustment magnitude should be <= ~5ms due to slew rate, got %v", videoAdj)
	require.Greater(t, -videoAdj, time.Duration(0),
		"video adjustment should be non-zero after 1s with 200ms offset, got %v", videoAdj)
}

func TestParticipantSync_RemoveTrack(t *testing.T) {
	ps := NewParticipantSync()

	baseNtp := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	audioEst := readyEstimator(48000, baseNtp, 0, 5)
	videoEst := readyEstimator(90000, baseNtp.Add(100*time.Millisecond), 0, 5)

	ps.SetTrackEstimator("audio-1", MediaTypeAudio, audioEst)
	ps.SetTrackEstimator("video-1", MediaTypeVideo, videoEst)

	ps.OnSenderReport("audio-1")
	ps.OnSenderReport("video-1")

	// Drive some updates so adjustments are non-zero.
	for i := 0; i <= 50; i++ {
		ps.updateAdjustments(time.Duration(i) * 100 * time.Millisecond)
	}

	// Verify video has some non-zero adjustment.
	require.NotEqual(t, time.Duration(0), ps.GetAdjustment("video-1"),
		"video adjustment should be non-zero before removal")

	// Remove the video track.
	ps.RemoveTrack("video-1")

	// After removal, GetAdjustment should return 0.
	require.Equal(t, time.Duration(0), ps.GetAdjustment("video-1"),
		"video adjustment should be zero after removal")

	// Audio should also return zero since there's no counterpart to sync against.
	// (But audio adjustment was always zero since audio is the reference.)
	require.Equal(t, time.Duration(0), ps.GetAdjustment("audio-1"),
		"audio adjustment should be zero (reference track)")
}
