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

func TestFailoverAttempts(t *testing.T) {
	cases := []struct {
		cfg  failoverConfig
		host string
		want int
	}{
		// Enabled (the default): only *.livekit.cloud project domains fail over.
		{failoverConfig{enabled: true}, "myproject.livekit.cloud", failoverMaxAttempts},
		{failoverConfig{enabled: true}, "myproject.region.livekit.cloud", failoverMaxAttempts},
		{failoverConfig{enabled: true}, "myproject.livekit.io", 1},
		{failoverConfig{enabled: true}, "example.com", 1},
		{failoverConfig{enabled: true}, "127.0.0.1", 1},
		{failoverConfig{enabled: true}, "notlivekit.cloud", 1},
		// force bypasses the cloud-host check; disabled never fails over.
		{failoverConfig{enabled: true, force: true}, "127.0.0.1", failoverMaxAttempts},
		{failoverConfig{enabled: false, force: true}, "myproject.livekit.cloud", 1},
		{failoverConfig{enabled: false}, "myproject.livekit.cloud", 1},
	}
	for _, c := range cases {
		if got := c.cfg.attempts(c.host); got != c.want {
			t.Errorf("attempts(cfg=%+v, host=%q) = %v, want %v", c.cfg, c.host, got, c.want)
		}
	}
}
