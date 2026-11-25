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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
)

// A deadline long enough for failover is detached from the context, and only
// failoverTransport re-applies it. A client with its own HTTP client must
// still end the call at the deadline.
func TestAgentClientKeepsDeadline(t *testing.T) {
	const deadline = 100 * time.Millisecond
	setMinFailoverTimeout(t, 10*time.Millisecond) // the deadline is long enough to detach

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	t.Setenv("LK_AGENTS_URL", srv.URL)

	for name, opts := range map[string][]AgentClientOption{
		"default HTTP client": nil,
		"own HTTP client":     {WithHTTPClient(&http.Client{})},
	} {
		t.Run(name, func(t *testing.T) {
			client, err := NewAgentClient("https://project.livekit.cloud", "key", "secret_secret_secret_secret_secret", opts...)
			if err != nil {
				t.Fatalf("NewAgentClient: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()

			start := time.Now()
			_, err = client.ListAgents(ctx, &livekit.ListAgentsRequest{})
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("ListAgents returned after %v, want about %v", elapsed, deadline)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ListAgents error = %v, want context.DeadlineExceeded", err)
			}
		})
	}
}
