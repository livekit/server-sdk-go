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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/server-sdk-go/v2/signalling"
)

// stallRegion is a fake LiveKit signal endpoint that accepts the WebSocket,
// sends a JoinResponse so the client configures its peer connections and starts
// the signal read loop, and then never negotiates. This reproduces the failure
// mode the region-fallback logic must handle: signaling succeeds but the peer
// connection never connects, so the client times out and tries the next region.
type stallRegion struct {
	server     *httptest.Server
	wsURL      string
	dialCount  atomic.Int32
	leaveCount atomic.Int32
}

func newStallRegion(t *testing.T) *stallRegion {
	t.Helper()
	sr := &stallRegion{}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	sr.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rtc") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		sr.dialCount.Inc()

		// Send a JoinResponse so the client treats signaling as established and
		// begins waiting for the peer connection (which never comes).
		join := &livekit.SignalResponse{
			Message: &livekit.SignalResponse_Join{
				Join: &livekit.JoinResponse{
					Room:              &livekit.Room{Sid: "RM_stall", Name: "stall"},
					Participant:       &livekit.ParticipantInfo{Sid: "PA_stall", Identity: "stall"},
					SubscriberPrimary: true,
					ServerInfo:        &livekit.ServerInfo{Edition: livekit.ServerInfo_Standard},
				},
			},
		}
		payload, err := proto.Marshal(join)
		if err != nil {
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			return
		}

		// Drain client messages until it gives up and closes the socket. Count
		// the leave the client sends on cleanup so the test can assert the
		// stalled connection was torn down before moving on.
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			req := &livekit.SignalRequest{}
			if proto.Unmarshal(data, req) == nil {
				if _, ok := req.Message.(*livekit.SignalRequest_Leave); ok {
					sr.leaveCount.Inc()
				}
			}
		}
	}))
	t.Cleanup(sr.server.Close)
	sr.wsURL = "ws" + strings.TrimPrefix(sr.server.URL, "http")
	return sr
}

// signalFailRegion is a fake region whose signal endpoint is unreachable: both
// the WebSocket upgrade (/rtc) and the validate probe (/rtc/validate) return
// 503. This reproduces a region that fails the very first signal request, so
// JoinContext errors out before the peer-connection stage and the client must
// fall back to another region.
type signalFailRegion struct {
	server           *httptest.Server
	wsURL            string
	wsAttempts       atomic.Int32
	validateAttempts atomic.Int32
}

func newSignalFailRegion(t *testing.T) *signalFailRegion {
	t.Helper()
	sr := &signalFailRegion{}
	sr.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rtc":
			sr.wsAttempts.Inc()
		case "/rtc/validate":
			sr.validateAttempts.Inc()
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(sr.server.Close)
	sr.wsURL = "ws" + strings.TrimPrefix(sr.server.URL, "http")
	return sr
}

// withMockRegions points region discovery at an in-test mock that serves the
// given region URLs from /settings/regions, and makes the initial (cloud) URL
// resolve as a cloud host so the fallback path activates. It restores the
// production behavior on cleanup.
func withMockRegions(t *testing.T, regionURLs []string) (initialURL string) {
	t.Helper()

	rs := &livekit.RegionSettings{}
	for i, u := range regionURLs {
		rs.Regions = append(rs.Regions, &livekit.RegionInfo{
			Region:   u,
			Url:      u,
			Distance: int64(i),
		})
	}
	body, err := protojson.Marshal(rs)
	require.NoError(t, err)

	var hits atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/settings/regions", r.URL.Path)
		hits.Inc()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(mock.Close)

	origIsCloud := isCloud
	origURL := regionSettingsURL
	isCloud = func(string) bool { return true }
	regionSettingsURL = func(string) string { return mock.URL + "/settings/regions" }
	t.Cleanup(func() {
		isCloud = origIsCloud
		regionSettingsURL = origURL
		require.Greater(t, hits.Load(), int32(0), "/settings/regions should have been queried")
	})

	// The initial URL just needs to parse as a (now always-cloud) host so the
	// region-discovery branch runs; we connect through the discovered regions.
	return host
}

// TestRegionFallbackConnects verifies the core reconnection requirement: when an
// initial region accepts signaling but the peer connection cannot connect within
// the timeout, the client refreshes regions and retries the next one, and once a
// healthy region is reached the full WebRTC connection is established and stays
// connected.
//
// Requires a running livekit-server (provided by `mage test`); skipped otherwise.
func TestRegionFallbackConnects(t *testing.T) {
	if apiKey == "" || apiSecret == "" {
		t.Skip("no LIVEKIT_KEYS; requires a running livekit-server")
	}

	bad1 := newStallRegion(t)
	bad2 := newStallRegion(t)
	// good region is the real livekit-server the integration suite runs against
	initialURL := withMockRegions(t, []string{bad1.wsURL, bad2.wsURL, host})

	var disconnects atomic.Int32
	var reconnecting atomic.Int32
	cb := &RoomCallback{
		OnDisconnected: func() { disconnects.Inc() },
		OnReconnecting: func() { reconnecting.Inc() },
	}

	// Default joinTimeout (15s) is left untouched on purpose: each stalled
	// attempt must be bounded by the per-attempt ConnectTimeout, not joinTimeout.
	room := NewRoom(cb)
	const connectTimeout = 2 * time.Second

	start := time.Now()
	err := room.Join(initialURL, ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            "region-fallback-" + t.Name(),
		ParticipantIdentity: "region-fallback-sub",
	}, WithConnectTimeout(connectTimeout))
	elapsed := time.Since(start)
	require.NoError(t, err, "should connect via the healthy region after the stalled ones")
	defer room.Disconnect()

	t.Logf("connected after %s", elapsed)

	// Each stalled region must give up at ConnectTimeout, not joinTimeout. Two
	// stalled attempts at 2s + backoff + a real connect is well under 10s; the
	// pre-fix behavior (joinTimeout=15s per attempt, ConnectTimeout ignored)
	// would take 30s+.
	require.Less(t, elapsed, 10*time.Second, "stalled regions must honor ConnectTimeout, not joinTimeout")

	// both stalled regions were attempted and torn down before falling through
	require.EqualValues(t, 1, bad1.dialCount.Load(), "first region should be attempted")
	require.EqualValues(t, 1, bad2.dialCount.Load(), "second region should be attempted")
	require.EqualValues(t, 1, bad1.leaveCount.Load(), "stalled region 1 must be cleaned up before retrying")
	require.EqualValues(t, 1, bad2.leaveCount.Load(), "stalled region 2 must be cleaned up before retrying")

	// the full WebRTC connection is established against the healthy region
	require.Equal(t, ConnectionStateConnected, room.ConnectionState())
	require.True(t, room.engine.IsConnected(), "peer connection should be connected")

	// regression guard for #916: the connection must not be torn down right
	// after it is established (stale region state used to close the new socket).
	require.Never(t, func() bool {
		return room.ConnectionState() != ConnectionStateConnected || room.engine.IsConnected() == false
	}, 3*time.Second, 200*time.Millisecond, "connection should stay up after region fallback")
	require.EqualValues(t, 0, disconnects.Load(), "should not disconnect after a successful fallback connect")
}

// TestRegionFallbackSignalFailure verifies the fallback path when the initial
// region fails the signal request itself (the WebSocket connection cannot be
// established), rather than timing out the peer connection. After discovering
// regions via /settings/regions, the client attempts the first region, fails to
// connect signaling, and reconnects to the next region to establish the full
// connection.
//
// Requires a running livekit-server (provided by `mage test`); skipped otherwise.
func TestRegionFallbackSignalFailure(t *testing.T) {
	if apiKey == "" || apiSecret == "" {
		t.Skip("no LIVEKIT_KEYS; requires a running livekit-server")
	}

	bad := newSignalFailRegion(t)
	// good region is the real livekit-server the integration suite runs against
	initialURL := withMockRegions(t, []string{bad.wsURL, host})

	// The bad region fails the signal request immediately (bad handshake), so it
	// never reaches the peer-connection wait; no timeout tuning is needed.
	var disconnects atomic.Int32
	room := NewRoom(&RoomCallback{OnDisconnected: func() { disconnects.Inc() }})

	start := time.Now()
	err := room.Join(initialURL, ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            "region-signalfail-" + t.Name(),
		ParticipantIdentity: "region-signalfail-sub",
	})
	require.NoError(t, err, "should connect via the healthy region after the signal failure")
	defer room.Disconnect()
	t.Logf("connected after %s", time.Since(start))

	// the unreachable region's signal request was attempted before falling through
	require.EqualValues(t, 1, bad.wsAttempts.Load(), "initial signal request should be attempted")

	// the full WebRTC connection is established against the healthy region
	require.Equal(t, ConnectionStateConnected, room.ConnectionState())
	require.True(t, room.engine.IsConnected(), "peer connection should be connected")

	require.Never(t, func() bool {
		return room.ConnectionState() != ConnectionStateConnected || room.engine.IsConnected() == false
	}, 2*time.Second, 200*time.Millisecond, "connection should stay up after signal-failure fallback")
	require.EqualValues(t, 0, disconnects.Load(), "should not disconnect after a successful fallback connect")
}

type recoveryRecorder struct {
	engineHandler
	mu     sync.Mutex
	events []string
}

func (r *recoveryRecorder) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recoveryRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *recoveryRecorder) OnResuming()                             { r.record("OnResuming") }
func (r *recoveryRecorder) OnRestarting()                           { r.record("OnRestarting") }
func (r *recoveryRecorder) OnDisconnected(livekit.DisconnectReason) { r.record("OnDisconnected") }

func TestResumeFailureEscalationFiresRestarting(t *testing.T) {
	region := newSignalFailRegion(t)
	recorder := &recoveryRecorder{}
	e := NewRTCEngine(false, recorder, func() string { return "PA_test" }, nil)
	e.connectionManager.setIncomingRequestParams(context.Background(), region.wsURL, "token", &signalling.ConnectParams{}, true)
	e.connectionManager.setConnected(&livekit.RegionInfo{Url: region.wsURL})
	t.Cleanup(func() { e.closed.Store(true) })

	e.handleDisconnect("test", false, nil)

	require.Eventually(t, func() bool {
		return slices.Contains(recorder.recorded(), "OnRestarting")
	}, time.Second, 10*time.Millisecond, "escalating a failed resume must announce the restart")
	got := recorder.recorded()
	require.Equal(t, []string{"OnResuming", "OnRestarting"}, got[:min(2, len(got))])
}

func TestRecoveryAnnouncesReconnectingOncePerOutage(t *testing.T) {
	var reconnecting, reconnected int
	cb := NewRoomCallback()
	cb.OnReconnecting = func() { reconnecting++ }
	cb.OnReconnected = func() { reconnected++ }
	r := NewRoom(cb)

	r.OnResuming()
	r.OnRestarting()
	r.OnRestarted(&livekit.Room{}, &livekit.ParticipantInfo{}, nil)
	r.OnRestarted(&livekit.Room{}, &livekit.ParticipantInfo{}, nil)
	r.OnRestartConnected()
	r.OnResuming()
	r.OnResumed()
	require.Equal(t, 2, reconnecting)
	require.Equal(t, 2, reconnected)
}

func TestRestartFailoverReportsReconnectedOnceConnected(t *testing.T) {
	if apiKey == "" || apiSecret == "" {
		t.Skip("no LIVEKIT_KEYS; requires a running livekit-server")
	}
	stall := newStallRegion(t)
	const connectTimeout = 2 * time.Second

	var reconnected atomic.Int32
	var stateAtReconnected atomic.String
	var room *Room
	room = NewRoom(&RoomCallback{
		OnReconnected: func() {
			reconnected.Inc()
			stateAtReconnected.Store(string(room.ConnectionState()))
		},
	})
	require.NoError(t, room.Join(host, ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            "restart-failover-" + t.Name(),
		ParticipantIdentity: "restart-failover",
	}, WithConnectTimeout(connectTimeout)))
	defer room.Disconnect()

	room.engine.handleDisconnect("test", true, &livekit.RegionSettings{
		Regions: []*livekit.RegionInfo{{Region: "stall", Url: stall.wsURL}},
	})

	require.Eventually(t, func() bool {
		return reconnected.Load() > 0 && room.ConnectionState() == ConnectionStateConnected
	}, 20*time.Second, 50*time.Millisecond, "the restart should fall through the stalled region and recover")
	require.EqualValues(t, 1, stall.dialCount.Load(), "the restart must try the stalled region first")
	require.EqualValues(t, 1, reconnected.Load(), "one restart reports one recovery")
	require.Equal(t, string(ConnectionStateConnected), stateAtReconnected.Load(), "OnReconnected must fire once the connection is up")
}
