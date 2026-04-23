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
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ntpToUint64 converts a time.Time to a 64-bit NTP timestamp.
// Upper 32 bits = seconds since NTP epoch (1900-01-01),
// lower 32 bits = fractional seconds.
func ntpToUint64(t time.Time) uint64 {
	const ntpEpochOffset = 2208988800
	secs := uint64(t.Unix()) + ntpEpochOffset
	frac := uint64(t.Nanosecond()) * (1 << 32) / 1e9
	return secs<<32 | frac
}

func TestNtpEstimator_NotReadyBeforeTwoSRs(t *testing.T) {
	e := NewNtpEstimator(90000)

	// Zero SRs: not ready
	require.False(t, e.IsReady(), "should not be ready with 0 SRs")

	_, err := e.RtpToNtp(1000)
	require.Error(t, err, "RtpToNtp should error when not ready")

	// One SR: still not ready
	now := time.Now()
	e.OnSenderReport(ntpToUint64(now), 90000, now)
	require.False(t, e.IsReady(), "should not be ready with 1 SR")

	_, err = e.RtpToNtp(90000)
	require.Error(t, err, "RtpToNtp should error with only 1 SR")

	// Two SRs: ready
	now2 := now.Add(time.Second)
	e.OnSenderReport(ntpToUint64(now2), 180000, now2)
	require.True(t, e.IsReady(), "should be ready with 2 SRs")

	_, err = e.RtpToNtp(135000)
	require.NoError(t, err, "RtpToNtp should succeed when ready")
}

func TestNtpEstimator_AccurateMapping(t *testing.T) {
	const clockRate = 90000
	e := NewNtpEstimator(clockRate)

	baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Feed 10 perfect SRs at 1-second intervals
	for i := 0; i < 10; i++ {
		wallTime := baseTime.Add(time.Duration(i) * time.Second)
		rtpTS := uint32(i) * clockRate
		e.OnSenderReport(ntpToUint64(wallTime), rtpTS, wallTime)
	}

	require.True(t, e.IsReady())

	// Verify mapping at intermediate points
	for _, tc := range []struct {
		name    string
		rtpTS   uint32
		wantNTP time.Time
	}{
		{"at SR 0", 0, baseTime},
		{"at SR 5", 5 * clockRate, baseTime.Add(5 * time.Second)},
		{"between SR 2 and 3", uint32(2.5 * clockRate), baseTime.Add(2500 * time.Millisecond)},
		{"at SR 9", 9 * clockRate, baseTime.Add(9 * time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.RtpToNtp(tc.rtpTS)
			require.NoError(t, err)
			diff := got.Sub(tc.wantNTP)
			if diff < 0 {
				diff = -diff
			}
			require.Less(t, diff, time.Millisecond,
				"mapping off by %v; got %v, want %v", diff, got, tc.wantNTP)
		})
	}
}

func TestNtpEstimator_OutlierRejection(t *testing.T) {
	const clockRate = 90000
	e := NewNtpEstimator(clockRate)

	baseTime := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)

	// Feed 5 good SRs at 1-second intervals
	for i := 0; i < 5; i++ {
		wallTime := baseTime.Add(time.Duration(i) * time.Second)
		rtpTS := uint32(i) * clockRate
		e.OnSenderReport(ntpToUint64(wallTime), rtpTS, wallTime)
	}

	require.True(t, e.IsReady())

	// Feed 1 wildly wrong SR: RTP says 5 seconds but NTP says 50 seconds
	badWallTime := baseTime.Add(50 * time.Second)
	badRTP := uint32(5) * clockRate
	e.OnSenderReport(ntpToUint64(badWallTime), badRTP, badWallTime)

	// Verify mapping is still accurate (outlier should have been rejected)
	got, err := e.RtpToNtp(uint32(2.5 * clockRate))
	require.NoError(t, err)
	want := baseTime.Add(2500 * time.Millisecond)
	diff := got.Sub(want)
	if diff < 0 {
		diff = -diff
	}
	require.Less(t, diff, time.Millisecond,
		"mapping should be accurate despite outlier; off by %v", diff)
}

func TestNtpEstimator_Wraparound(t *testing.T) {
	const clockRate = 90000
	e := NewNtpEstimator(clockRate)

	baseTime := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)

	// Start RTP near uint32 max so wraparound occurs
	// math.MaxUint32 - 5*clockRate puts us 5 seconds before wrap
	startRTP := uint32(math.MaxUint32 - 5*clockRate)

	for i := 0; i < 10; i++ {
		wallTime := baseTime.Add(time.Duration(i) * time.Second)
		rtpTS := startRTP + uint32(i)*clockRate // will wrap around uint32
		e.OnSenderReport(ntpToUint64(wallTime), rtpTS, wallTime)
	}

	require.True(t, e.IsReady())

	// Test mapping at points before and after the wraparound
	// SR at i=5 is exactly where RTP wraps past 0
	for _, tc := range []struct {
		name    string
		idx     int
		wantNTP time.Time
	}{
		{"before wrap (i=3)", 3, baseTime.Add(3 * time.Second)},
		{"at wrap (i=5)", 5, baseTime.Add(5 * time.Second)},
		{"after wrap (i=8)", 8, baseTime.Add(8 * time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rtpTS := startRTP + uint32(tc.idx)*clockRate
			got, err := e.RtpToNtp(rtpTS)
			require.NoError(t, err)
			diff := got.Sub(tc.wantNTP)
			if diff < 0 {
				diff = -diff
			}
			require.Less(t, diff, time.Millisecond,
				"mapping off by %v across wraparound; got %v, want %v", diff, got, tc.wantNTP)
		})
	}
}

func TestNtpEstimator_SlidingWindow(t *testing.T) {
	const clockRate = 90000
	e := NewNtpEstimator(clockRate)

	baseTime := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)

	// Feed 25 SRs (exceeds window of 20)
	for i := 0; i < 25; i++ {
		wallTime := baseTime.Add(time.Duration(i) * time.Second)
		rtpTS := uint32(i) * clockRate
		e.OnSenderReport(ntpToUint64(wallTime), rtpTS, wallTime)
	}

	require.True(t, e.IsReady())

	// Verify mapping still works accurately in the recent window
	got, err := e.RtpToNtp(uint32(22) * clockRate)
	require.NoError(t, err)
	want := baseTime.Add(22 * time.Second)
	diff := got.Sub(want)
	if diff < 0 {
		diff = -diff
	}
	require.Less(t, diff, time.Millisecond,
		"mapping should be accurate after sliding window overflow; off by %v", diff)
}

func TestNtpEstimator_Slope(t *testing.T) {
	const clockRate = 90000
	e := NewNtpEstimator(clockRate)

	baseTime := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)

	// Feed 5 perfect SRs
	for i := 0; i < 5; i++ {
		wallTime := baseTime.Add(time.Duration(i) * time.Second)
		rtpTS := uint32(i) * clockRate
		e.OnSenderReport(ntpToUint64(wallTime), rtpTS, wallTime)
	}

	require.True(t, e.IsReady())

	// Slope should be close to 1/clockRate (seconds per RTP tick)
	expectedSlope := 1.0 / float64(clockRate)
	gotSlope := e.Slope()

	relError := math.Abs(gotSlope-expectedSlope) / expectedSlope
	require.Less(t, relError, 1e-6,
		"slope should be ~%e, got %e (relative error %e)", expectedSlope, gotSlope, relError)
}
