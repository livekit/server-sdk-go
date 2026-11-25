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

// Package agentsurl holds what the SDK's cloud-agents clients share about the
// cloud-agents URL: lksdk.AgentClient for Twirp calls, and pkg/cloudagents for
// builds and logs.
package agentsurl

import "net/url"

// IsLocal reports whether rawURL points at a local development server. The
// clients send requests to such a URL as it is, instead of to the agents host
// that they derive from a project URL.
func IsLocal(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1"
}
