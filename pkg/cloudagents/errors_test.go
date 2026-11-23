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

package cloudagents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	lkproto "github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

// newTestClient returns a Client whose cloud-agents is srv.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	t.Setenv("LK_AGENTS_URL", srv.URL)
	c, err := New(WithProject("https://project.livekit.cloud", "test-api-key", "test-api-secret"), WithHTTPClient(srv.Client()), WithLogger(logger.GetLogger()))
	require.NoError(t, err)
	return c
}

// answer returns a server that answers every request with status and body.
func answer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Each route's HTTPError keeps the text that the route returned before.
func TestHTTPError(t *testing.T) {
	const jsonBody = `{"msg":"build rate limit exceeded","meta":{"project_id":"p_1"}}` + "\n"
	tests := []struct {
		name    string
		status  int
		body    string
		call    func(c *Client) error
		text    string
		message string
		meta    map[string]string
	}{
		{
			name:   "build",
			status: http.StatusTooManyRequests,
			body:   jsonBody,
			call: func(c *Client) error {
				return c.build(context.Background(), "CA_1", nil, "", &bytes.Buffer{})
			},
			text:    "failed to build agent: 429 Too Many Requests",
			message: "build rate limit exceeded",
			meta:    map[string]string{"project_id": "p_1"},
		},
		{
			name:   "build without a JSON body",
			status: http.StatusBadGateway,
			body:   "bad gateway",
			call: func(c *Client) error {
				return c.build(context.Background(), "CA_1", nil, "", &bytes.Buffer{})
			},
			text: "failed to build agent: 502 Bad Gateway",
		},
		{
			name:   "logs",
			status: http.StatusTooManyRequests,
			body:   jsonBody,
			call: func(c *Client) error {
				return c.StreamLogs(context.Background(), "build", "CA_1", "", &bytes.Buffer{}, "")
			},
			text:    "failed to get logs: build rate limit exceeded - map[project_id:p_1]",
			message: "build rate limit exceeded",
			meta:    map[string]string{"project_id": "p_1"},
		},
		{
			name:   "logs without a JSON body",
			status: http.StatusBadGateway,
			body:   "bad gateway",
			call: func(c *Client) error {
				return c.StreamLogs(context.Background(), "build", "CA_1", "", &bytes.Buffer{}, "")
			},
			text: "failed to parse error response: invalid character 'b' looking for beginning of value",
		},
		{
			name:   "push-target",
			status: http.StatusNotFound,
			body:   `{"msg":"agent not found"}`,
			call: func(c *Client) error {
				_, err := c.GetPushTarget(context.Background(), "CA_1")
				return err
			},
			text:    `push-target returned 404: {"msg":"agent not found"}`,
			message: "agent not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(newTestClient(t, answer(t, tt.status, tt.body)))

			var httpErr *HTTPError
			require.True(t, errors.As(err, &httpErr), "error %v is not an HTTPError", err)
			require.Equal(t, tt.text, err.Error())
			require.Equal(t, tt.status, httpErr.StatusCode)
			require.Equal(t, fmt.Sprintf("%d %s", tt.status, http.StatusText(tt.status)), httpErr.Status)
			require.Equal(t, tt.message, httpErr.Message)
			require.Equal(t, tt.meta, httpErr.Meta)
			require.Equal(t, tt.body, string(httpErr.Body))
		})
	}
}

func TestBuildError(t *testing.T) {
	srv := answer(t, http.StatusOK, `{"lkQueue":{"message":"queued"}}`+"\nBUILD ERROR: timed out waiting for an available builder, please try again.\n")
	err := newTestClient(t, srv).build(context.Background(), "CA_1", nil, "", &bytes.Buffer{})

	var buildErr *BuildError
	require.True(t, errors.As(err, &buildErr), "error %v is not a BuildError", err)
	require.Equal(t, "timed out waiting for an available builder, please try again.", err.Error())
	require.Equal(t, "timed out waiting for an available builder, please try again.", buildErr.Message)
}

func TestRefusedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var res proto.Message
		switch r.URL.Path {
		case "/twirp/livekit.CloudAgent/DeployAgent":
			res = &lkproto.DeployAgentResponse{Message: "agent is already building"}
		case "/twirp/livekit.CloudAgent/DeployAgentV2":
			res = &lkproto.DeployAgentV2Response{Message: "agent is already building"}
		case "/twirp/livekit.CloudAgent/PromoteAgent":
			res = &lkproto.PromoteAgentResponse{Message: "nothing to promote"}
		default:
			http.NotFound(w, r)
			return
		}
		b, err := proto.Marshal(res)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/protobuf")
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	tests := []struct {
		name string
		err  error
		op   string
		text string
	}{
		{"DeployAgent", c.DeployAgent(ctx, "CA_1", nil, nil, nil, nil, nil), "deploy agent", "failed to deploy agent: agent is already building"},
		{"DeployAgentV2", c.DeployAgentV2(ctx, "CA_1", nil, nil, nil, "", nil, nil), "deploy agent", "failed to deploy agent: agent is already building"},
		{"PromoteAgent", c.PromoteAgent(ctx, "CA_1", "staging", "production"), "promote agent", "failed to promote agent: nothing to promote"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var refused *RefusedError
			require.True(t, errors.As(tt.err, &refused), "error %v is not a RefusedError", tt.err)
			require.Equal(t, tt.op, refused.Op)
			require.Equal(t, tt.text, tt.err.Error())
		})
	}
}
