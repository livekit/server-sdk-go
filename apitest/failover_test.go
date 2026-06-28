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

// Package apitest holds black-box API tests that exercise the public server SDK
// against the shared mock LiveKit API server (livekit/livekit cmd/test-server).
// Point them at a running instance with LK_TEST_SERVER_URL (default
// http://127.0.0.1:9999); they skip when no server is reachable. In CI the
// server is booted as a Docker container.
//
// See cmd/test-server/README.md for the X-Lk-Mock-* control protocol. Mock
// directives are passed to the SDK via twirp request headers, which the
// failover transport forwards to the discovery fetch and every retry.
package apitest

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/twitchtv/twirp"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	hdrFailRegions = "X-Lk-Mock-Fail-Regions"
	hdrFailMode    = "X-Lk-Mock-Fail-Mode"
	hdrFailStatus  = "X-Lk-Mock-Fail-Status"
	hdrRegionsStat = "X-Lk-Mock-Regions-Status"
)

func testServerURL(t *testing.T) string {
	url := os.Getenv("LK_TEST_SERVER_URL")
	if url == "" {
		url = "http://127.0.0.1:9999"
	}
	resp, err := http.Get(url + "/settings/regions")
	if err != nil {
		t.Skipf("mock test server not reachable at %s (set LK_TEST_SERVER_URL): %v", url, err)
	}
	_ = resp.Body.Close()
	return url
}

// failoverCtx returns a context that forces failover on (the mock is not a
// cloud host) with a tiny backoff, carrying the given X-Lk-Mock-* directives as
// twirp request headers.
func failoverCtx(t *testing.T, mode lksdk.FailoverMode, directives map[string]string) context.Context {
	ctx := lksdk.WithFailoverOptions(context.Background(), lksdk.FailoverOptions{
		Mode:        mode,
		BackoffBase: time.Millisecond,
	})
	if len(directives) > 0 {
		h := make(http.Header)
		for k, v := range directives {
			h.Set(k, v)
		}
		var err error
		ctx, err = twirp.WithHTTPRequestHeaders(ctx, h)
		require.NoError(t, err)
	}
	return ctx
}

func TestAPI_Healthy(t *testing.T) {
	client := lksdk.NewRoomServiceClient(testServerURL(t), "devkey", "secret")
	room, err := client.CreateRoom(failoverCtx(t, lksdk.FailoverOn, nil), &livekit.CreateRoomRequest{Name: "api-test"})
	require.NoError(t, err)
	require.Equal(t, "api-test", room.Name, "the mock echoes the request name")
	require.NotEmpty(t, room.Sid)
}

func TestAPI_PrimaryUnavailable(t *testing.T) {
	client := lksdk.NewRoomServiceClient(testServerURL(t), "devkey", "secret")
	ctx := failoverCtx(t, lksdk.FailoverOn, map[string]string{hdrFailRegions: "0"})
	_, err := client.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "api-test"})
	require.NoError(t, err, "should fail over to a healthy region")
}

func TestAPI_TwoRegionsUnavailable(t *testing.T) {
	client := lksdk.NewRoomServiceClient(testServerURL(t), "devkey", "secret")
	ctx := failoverCtx(t, lksdk.FailoverOn, map[string]string{hdrFailRegions: "0,1"})
	_, err := client.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "api-test"})
	require.NoError(t, err, "should fail over to the third region")
}

func TestAPI_AllUnavailable(t *testing.T) {
	client := lksdk.NewRoomServiceClient(testServerURL(t), "devkey", "secret")
	ctx := failoverCtx(t, lksdk.FailoverOn, map[string]string{hdrFailRegions: "0,1,2,3"})
	_, err := client.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "api-test"})
	require.Error(t, err)
}

func TestAPI_ClientErrorNotRetried(t *testing.T) {
	client := lksdk.NewRoomServiceClient(testServerURL(t), "devkey", "secret")
	ctx := failoverCtx(t, lksdk.FailoverOn, map[string]string{hdrFailRegions: "0", hdrFailStatus: "400"})
	_, err := client.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "api-test"})
	require.Error(t, err)
	var terr twirp.Error
	require.ErrorAs(t, err, &terr)
	require.Equal(t, twirp.InvalidArgument, terr.Code(), "a 4xx must surface as a typed error, not fail over")
}

func TestAPI_TransportError(t *testing.T) {
	client := lksdk.NewRoomServiceClient(testServerURL(t), "devkey", "secret")
	ctx := failoverCtx(t, lksdk.FailoverOn, map[string]string{hdrFailRegions: "0", hdrFailMode: "drop"})
	_, err := client.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "api-test"})
	require.NoError(t, err, "a dropped connection should fail over to a healthy region")
}

func TestAPI_RegionDiscoveryUnreachable(t *testing.T) {
	client := lksdk.NewRoomServiceClient(testServerURL(t), "devkey", "secret")
	ctx := failoverCtx(t, lksdk.FailoverOn, map[string]string{hdrFailRegions: "0", hdrRegionsStat: "500"})
	_, err := client.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "api-test"})
	require.Error(t, err, "no fallback hosts means the original error is surfaced")
}

func TestAPI_FailoverDisabledForNonCloud(t *testing.T) {
	client := lksdk.NewRoomServiceClient(testServerURL(t), "devkey", "secret")
	// Auto mode against 127.0.0.1 (non-cloud) must not fail over.
	ctx := failoverCtx(t, lksdk.FailoverAuto, map[string]string{hdrFailRegions: "0"})
	_, err := client.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "api-test"})
	require.Error(t, err)
}
