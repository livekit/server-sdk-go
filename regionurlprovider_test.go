// Copyright 2024 LiveKit, Inc.
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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/protocol/livekit"
)

// regionSettingsServer is a fake LiveKit Cloud `/settings/regions` endpoint. It
// records how many times it was hit and serves a configurable region list.
type regionSettingsServer struct {
	server   *httptest.Server
	hostname string
	hits     atomic.Int32

	mu      sync.Mutex
	regions *livekit.RegionSettings
	status  int
}

func newRegionSettingsServer(t *testing.T, regions *livekit.RegionSettings) *regionSettingsServer {
	t.Helper()
	rs := &regionSettingsServer{
		regions: regions,
		status:  http.StatusOK,
	}
	rs.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/settings/regions", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		rs.hits.Add(1)

		rs.mu.Lock()
		status, regions := rs.status, rs.regions
		rs.mu.Unlock()

		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		b, err := protojson.Marshal(regions)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(rs.server.Close)
	// strip scheme so it matches what parseCloudURL produces (host[:port])
	rs.hostname = strings.TrimPrefix(rs.server.URL, "https://")
	return rs
}

func (rs *regionSettingsServer) setRegions(regions *livekit.RegionSettings) {
	rs.mu.Lock()
	rs.regions = regions
	rs.mu.Unlock()
}

func newTestProvider(rs *regionSettingsServer) *regionURLProvider {
	p := newRegionURLProvider()
	// trust the httptest TLS certificate
	p.httpClient = rs.server.Client()
	return p
}

func TestRegionURLProvider_FetchAndPopOrder(t *testing.T) {
	rs := newRegionSettingsServer(t, &livekit.RegionSettings{
		Regions: []*livekit.RegionInfo{
			{Region: "us-east", Url: "wss://us-east.example.com", Distance: 10},
			{Region: "us-west", Url: "wss://us-west.example.com", Distance: 50},
			{Region: "eu", Url: "wss://eu.example.com", Distance: 100},
		},
	})
	p := newTestProvider(rs)

	require.NoError(t, p.RefreshRegionSettings(rs.hostname, "test-token"))
	require.EqualValues(t, 1, rs.hits.Load(), "should hit /settings/regions once")

	// PopBestURL returns regions in the order the server provided them (server
	// sorts by proximity), removing each as it goes.
	for _, want := range []string{
		"wss://us-east.example.com",
		"wss://us-west.example.com",
		"wss://eu.example.com",
	} {
		got, err := p.PopBestURL(rs.hostname, "test-token")
		require.NoError(t, err)
		require.Equal(t, want, got)
	}

	// once exhausted, PopBestURL errors until RefreshRegionSettings repopulates
	_, err := p.PopBestURL(rs.hostname, "test-token")
	require.Error(t, err)
}

func TestRegionURLProvider_CacheAvoidsRefetch(t *testing.T) {
	rs := newRegionSettingsServer(t, &livekit.RegionSettings{
		Regions: []*livekit.RegionInfo{{Region: "a", Url: "wss://a.example.com"}},
	})
	p := newTestProvider(rs)

	require.NoError(t, p.RefreshRegionSettings(rs.hostname, "test-token"))
	require.NoError(t, p.RefreshRegionSettings(rs.hostname, "test-token"))
	require.EqualValues(t, 1, rs.hits.Load(), "second refresh within cache TTL must not refetch")
}

// TestRegionURLProvider_RefreshRepopulates verifies that after exhausting all
// regions, a refresh past the cache TTL fetches /settings/regions again and the
// fallback routine can continue with a fresh list. This backs the requirement
// that retries "continue the same routine with /settings/regions".
func TestRegionURLProvider_RefreshRepopulates(t *testing.T) {
	rs := newRegionSettingsServer(t, &livekit.RegionSettings{
		Regions: []*livekit.RegionInfo{{Region: "a", Url: "wss://a.example.com"}},
	})
	p := newTestProvider(rs)

	require.NoError(t, p.RefreshRegionSettings(rs.hostname, "test-token"))
	got, err := p.PopBestURL(rs.hostname, "test-token")
	require.NoError(t, err)
	require.Equal(t, "wss://a.example.com", got)
	_, err = p.PopBestURL(rs.hostname, "test-token")
	require.Error(t, err, "exhausted")

	// expire the cache and serve a different region list
	p.mutex.Lock()
	p.hostnameSettingsCache[rs.hostname].updatedAt = time.Now().Add(-2 * regionHostnameProviderSettingsCacheTime)
	p.mutex.Unlock()
	rs.setRegions(&livekit.RegionSettings{
		Regions: []*livekit.RegionInfo{{Region: "b", Url: "wss://b.example.com"}},
	})

	require.NoError(t, p.RefreshRegionSettings(rs.hostname, "test-token"))
	require.EqualValues(t, 2, rs.hits.Load(), "refresh past TTL must refetch")
	got, err = p.PopBestURL(rs.hostname, "test-token")
	require.NoError(t, err)
	require.Equal(t, "wss://b.example.com", got)
}

func TestRegionURLProvider_FetchError(t *testing.T) {
	rs := newRegionSettingsServer(t, &livekit.RegionSettings{})
	rs.mu.Lock()
	rs.status = http.StatusServiceUnavailable
	rs.mu.Unlock()
	p := newTestProvider(rs)

	err := p.RefreshRegionSettings(rs.hostname, "test-token")
	require.Error(t, err)

	_, err = p.PopBestURL(rs.hostname, "test-token")
	require.Error(t, err, "no regions cached after a failed fetch")
}
