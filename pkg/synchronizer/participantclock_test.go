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

func TestParticipantClock_SetAndRemoveTrack(t *testing.T) {
	st := NewSessionTimeline(nil)
	pc := st.AddParticipant("alice")

	e := readyEstimator(48000, time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC), 0, 5)
	pc.SetTrackEstimator("audio-1", e)

	pc.RemoveTrack("audio-1")
}

func TestParticipantClock_UpdateEstimator(t *testing.T) {
	st := NewSessionTimeline(nil)
	pc := st.AddParticipant("alice")

	baseNtp := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	e1 := readyEstimator(48000, baseNtp, 0, 5)
	e2 := readyEstimator(48000, baseNtp.Add(time.Second), 0, 5)

	pc.SetTrackEstimator("audio-1", e1)
	pc.SetTrackEstimator("audio-1", e2)

	// Should use e2, not e1.
	pc.mu.Lock()
	require.Same(t, e2, pc.tracks["audio-1"])
	pc.mu.Unlock()
}
