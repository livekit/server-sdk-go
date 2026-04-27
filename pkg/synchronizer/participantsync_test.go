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
	e := NewNtpEstimator(clockRate, nil)
	for i := 0; i < count; i++ {
		ntpTime := baseNtp.Add(time.Duration(i) * 5 * time.Second)
		rtpTS := baseRtp + uint32(i)*uint32(clockRate)*5
		e.OnSenderReport(ntpToUint64(ntpTime), rtpTS, ntpTime.Add(30*time.Millisecond))
	}
	return e
}

func TestParticipantSync_SetAndRemoveTrack(t *testing.T) {
	ps := NewParticipantSync()

	e := readyEstimator(48000, time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC), 0, 5)
	ps.SetTrackEstimator("audio-1", MediaTypeAudio, e)

	ps.RemoveTrack("audio-1")

	// Should not panic or error after removal.
	ps.OnSenderReport("audio-1")
}

func TestParticipantSync_UpdateEstimator(t *testing.T) {
	ps := NewParticipantSync()

	baseNtp := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	e1 := readyEstimator(48000, baseNtp, 0, 5)
	e2 := readyEstimator(48000, baseNtp.Add(time.Second), 0, 5)

	ps.SetTrackEstimator("audio-1", MediaTypeAudio, e1)
	ps.SetTrackEstimator("audio-1", MediaTypeAudio, e2)

	// Should use e2, not e1.
	ps.mu.Lock()
	entry := ps.tracks["audio-1"]
	require.Same(t, e2, entry.estimator)
	ps.mu.Unlock()
}
