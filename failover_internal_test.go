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

package lksdk

import "testing"

func TestFailoverEnabledFor(t *testing.T) {
	cases := []struct {
		mode FailoverMode
		host string
		want bool
	}{
		// Auto: only *.livekit.cloud project domains.
		{FailoverAuto, "myproject.livekit.cloud", true},
		{FailoverAuto, "myproject.region.livekit.cloud", true},
		{FailoverAuto, "myproject.livekit.io", false},
		{FailoverAuto, "example.com", false},
		{FailoverAuto, "127.0.0.1", false},
		{FailoverAuto, "notlivekit.cloud", false},
		// On/Off override the host check.
		{FailoverOn, "127.0.0.1", true},
		{FailoverOff, "myproject.livekit.cloud", false},
	}
	for _, c := range cases {
		if got := (FailoverConfig{Mode: c.mode}).enabledFor(c.host); got != c.want {
			t.Errorf("enabledFor(mode=%v, host=%q) = %v, want %v", c.mode, c.host, got, c.want)
		}
	}
}
