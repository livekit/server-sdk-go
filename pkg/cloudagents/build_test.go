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
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// queuedLine is the first line cloud-agents writes on the build stream of a v2
// client, once it has queued the build.
const queuedLine = `{"lkQueue":{"message":"This deploy continues even if you disconnect; run ` + "`lk agent status`" + ` to check."}}` + "\n"

// StartBuild returns at the first line, while the build stream is still open,
// and Close ends the stream.
func TestStartBuildReturnsAtFirstLine(t *testing.T) {
	requests := make(chan *http.Request, 1)
	disconnected := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		fmt.Fprint(w, queuedLine)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := c.StartBuild(ctx, "CA_1", BuildOptions{
		Deployment: "staging",
		Tag:        "v20261123",
		Attributes: map[string]string{"source": "builder"},
	})
	require.NoError(t, err)

	r := <-requests
	require.Equal(t, http.MethodPost, r.Method)
	require.Equal(t, "/build", r.URL.Path)
	require.Equal(t, "CA_1", r.URL.Query().Get("agent_id"))
	require.Equal(t, "staging", r.URL.Query().Get("deployment"))
	require.Equal(t, "v20261123", r.URL.Query().Get("tag"))
	require.Equal(t, "v2", r.Header.Get("X-LIVEKIT-BUILD-PROTOCOL"))
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(`{"source":"builder"}`)), r.Header.Get(attributesHeader))

	require.NoError(t, b.Close())
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the build stream")
	}
}

func TestStartBuildWithoutTag(t *testing.T) {
	var query map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		fmt.Fprint(w, queuedLine)
	}))
	defer srv.Close()

	b, err := newTestClient(t, srv).StartBuild(context.Background(), "CA_1", BuildOptions{})
	require.NoError(t, err)
	require.NoError(t, b.Close())
	require.Equal(t, map[string][]string{"agent_id": {"CA_1"}}, query)
}

// Wait renders the whole stream, including the first line that StartBuild read.
func TestBuildWait(t *testing.T) {
	srv := answer(t, http.StatusOK, queuedLine+`{"lkQueue":{"message":"Waiting for an available builder"}}`+"\n")

	b, err := newTestClient(t, srv).StartBuild(context.Background(), "CA_1", BuildOptions{})
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, b.Wait(context.Background(), &out))
	require.Equal(t, "This deploy continues even if you disconnect; run `lk agent status` to check.\nWaiting for an available builder\n", out.String())
}

func TestStartBuildErrors(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		srv := answer(t, http.StatusTooManyRequests, `{"msg":"build rate limit exceeded"}`)
		_, err := newTestClient(t, srv).StartBuild(context.Background(), "CA_1", BuildOptions{})
		var httpErr *HTTPError
		require.True(t, errors.As(err, &httpErr), "error %v is not an HTTPError", err)
		require.Equal(t, http.StatusTooManyRequests, httpErr.StatusCode)
		require.Equal(t, "build rate limit exceeded", httpErr.Message)
	})
	t.Run("build error as the first line", func(t *testing.T) {
		srv := answer(t, http.StatusOK, "BUILD ERROR: the build was superseded by a newer deploy\n")
		_, err := newTestClient(t, srv).StartBuild(context.Background(), "CA_1", BuildOptions{})
		var buildErr *BuildError
		require.True(t, errors.As(err, &buildErr), "error %v is not a BuildError", err)
		require.Equal(t, "the build was superseded by a newer deploy", buildErr.Message)
	})
	t.Run("stream closed before the first line", func(t *testing.T) {
		srv := answer(t, http.StatusOK, "")
		c := newTestClient(t, srv)
		_, err := c.StartBuild(context.Background(), "CA_1", BuildOptions{})
		require.ErrorIs(t, err, ErrBuildNotQueued)
		require.Equal(t, "the build stream closed before the build was queued", err.Error())

		// A deploy keeps its old behaviour: no error.
		require.NoError(t, c.build(context.Background(), "CA_1", nil, "", &bytes.Buffer{}))
	})
	t.Run("build error later", func(t *testing.T) {
		srv := answer(t, http.StatusOK, queuedLine+"BUILD ERROR: timed out waiting for an available builder, please try again.\n")
		b, err := newTestClient(t, srv).StartBuild(context.Background(), "CA_1", BuildOptions{})
		require.NoError(t, err)
		err = b.Wait(context.Background(), &bytes.Buffer{})
		var buildErr *BuildError
		require.True(t, errors.As(err, &buildErr), "error %v is not a BuildError", err)
		require.Equal(t, "timed out waiting for an available builder, please try again.", buildErr.Message)
	})
}

// Cancelling Wait's ctx stops reading a stream that is still open.
func TestBuildWaitCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, queuedLine)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	b, err := newTestClient(t, srv).StartBuild(context.Background(), "CA_1", BuildOptions{})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = b.Wait(ctx, &bytes.Buffer{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

// Wait reads a line longer than bufio.Scanner's 64 KiB default.
func TestBuildWaitLongLine(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	srv := answer(t, http.StatusOK, queuedLine+`{"lkQueue":{"message":"`+long+`"}}`+"\n")

	b, err := newTestClient(t, srv).StartBuild(context.Background(), "CA_1", BuildOptions{})
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, b.Wait(context.Background(), &out))
	require.True(t, strings.HasSuffix(out.String(), long+"\n"))
}
