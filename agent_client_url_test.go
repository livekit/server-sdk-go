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

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/livekit/protocol/livekit"
)

// newAgentsServer answers every Twirp call with an empty response and counts
// the calls.
func newAgentsServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/protobuf")
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestAgentClientURL(t *testing.T) {
	srv, calls := newAgentsServer(t)
	localhost := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)

	tests := []struct {
		name       string
		projectURL string
		env        string
		opts       []AgentClientOption
	}{
		{name: "127.0.0.1 project URL", projectURL: srv.URL},
		{name: "localhost project URL", projectURL: localhost},
		{name: "local ws project URL", projectURL: strings.Replace(srv.URL, "http", "ws", 1)},
		{name: "LK_AGENTS_URL", projectURL: "https://project.livekit.cloud", env: srv.URL},
		{
			name:       "WithAgentsURL overrides LK_AGENTS_URL",
			projectURL: "https://project.livekit.cloud",
			env:        "http://127.0.0.1:1",
			opts:       []AgentClientOption{WithAgentsURL(srv.URL)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LK_AGENTS_URL", tt.env)
			client, err := NewAgentClient(tt.projectURL, "key", "secret_secret_secret_secret_secret", tt.opts...)
			if err != nil {
				t.Fatalf("NewAgentClient: %v", err)
			}
			before := calls.Load()
			if _, err := client.ListAgents(context.Background(), &livekit.ListAgentsRequest{}); err != nil {
				t.Fatalf("ListAgents: %v", err)
			}
			if calls.Load() != before+1 {
				t.Fatal("ListAgents did not reach the local server")
			}
		})
	}
}
