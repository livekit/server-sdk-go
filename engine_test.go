package lksdk

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
)

func TestFilterTURNServers(t *testing.T) {
	tests := []struct {
		name     string
		input    []*livekit.ICEServer
		expected []*livekit.ICEServer
	}{
		{
			name: "removes turn and turns URLs, keeps stun",
			input: []*livekit.ICEServer{
				{
					Urls: []string{
						"turn:ip-10-0-0-1.host.livekit.cloud:3478?transport=udp",
						"turns:oashburn1b.turn.livekit.cloud:443?transport=tcp",
						"stun:stun.l.google.com:19302",
					},
					Username:   "user",
					Credential: "pass",
				},
			},
			expected: []*livekit.ICEServer{
				{
					Urls:       []string{"stun:stun.l.google.com:19302"},
					Username:   "user",
					Credential: "pass",
				},
			},
		},
		{
			name: "case insensitive",
			input: []*livekit.ICEServer{
				{
					Urls:       []string{"TURN:host:3478", "TURNS:host:443", "STUN:host:3478"},
					Username:   "user",
					Credential: "pass",
				},
			},
			expected: []*livekit.ICEServer{
				{
					Urls:       []string{"STUN:host:3478"},
					Username:   "user",
					Credential: "pass",
				},
			},
		},
		{
			name: "drops server entirely when all URLs are turn",
			input: []*livekit.ICEServer{
				{
					Urls: []string{
						"turn:host:3478",
						"turns:host:443?transport=tcp",
					},
					Username:   "user",
					Credential: "pass",
				},
			},
			expected: nil,
		},
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name: "multiple servers",
			input: []*livekit.ICEServer{
				{
					Urls:       []string{"turn:host:3478"},
					Username:   "user1",
					Credential: "pass1",
				},
				{
					Urls:       []string{"stun:stun.l.google.com:19302"},
					Username:   "",
					Credential: "",
				},
			},
			expected: []*livekit.ICEServer{
				{
					Urls:       []string{"stun:stun.l.google.com:19302"},
					Username:   "",
					Credential: "",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := filterTURNServers(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestBuildReconnectCandidates(t *testing.T) {
	regions := &livekit.RegionSettings{
		Regions: []*livekit.RegionInfo{
			{Url: "wss://r1"},
			{Url: "wss://r2"},
		},
	}
	tests := []struct {
		name           string
		serverDirected bool
		currentURL     string
		settings       *livekit.RegionSettings
		originalURL    string
		want           []string
	}{
		{
			name:        "current first, then regions, then global",
			currentURL:  "wss://current",
			settings:    regions,
			originalURL: "wss://global",
			want:        []string{"wss://current", "wss://r1", "wss://r2", "wss://global"},
		},
		{
			name:           "server-directed skips current",
			serverDirected: true,
			currentURL:     "wss://current",
			settings:       regions,
			originalURL:    "wss://global",
			want:           []string{"wss://r1", "wss://r2", "wss://global"},
		},
		{
			name:        "dedupes current when it is also the geo-best region",
			currentURL:  "wss://r1",
			settings:    regions,
			originalURL: "wss://global",
			want:        []string{"wss://r1", "wss://r2", "wss://global"},
		},
		{
			name:        "nil settings falls back to current and global",
			currentURL:  "wss://current",
			settings:    nil,
			originalURL: "wss://global",
			want:        []string{"wss://current", "wss://global"},
		},
		{
			name:           "server-directed with nil settings tries only global",
			serverDirected: true,
			currentURL:     "wss://current",
			settings:       nil,
			originalURL:    "wss://global",
			want:           []string{"wss://global"},
		},
		{
			name:        "drops empty urls",
			currentURL:  "",
			settings:    regions,
			originalURL: "",
			want:        []string{"wss://r1", "wss://r2"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildReconnectCandidates(tc.serverDirected, tc.currentURL, tc.settings, tc.originalURL)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestConnectWithRegionFailover drives the full-reconnect walk with a mocked
// per-candidate connect, proving region fallback without a real server: the
// current region is skipped when the server directs the reconnect, failed
// regions are walked past, and the first working region wins.
func TestConnectWithRegionFailover(t *testing.T) {
	const host = "test.livekit.cloud"
	mockRegions := &livekit.RegionSettings{
		Regions: []*livekit.RegionInfo{
			{Url: "wss://region-b"},
			{Url: "wss://region-c"},
		},
	}
	newEngine := func(serverDirected bool) *RTCEngine {
		e := &RTCEngine{
			log:            logger,
			regionProvider: newRegionURLProvider(),
			cloudHost:      host,
			url:            "wss://region-a", // current region
			originalURL:    "wss://global",
		}
		e.token.Store("tok")
		// server-pushed list (excludes the current region) — fresh, so the walk
		// uses it without a network fetch
		e.regionProvider.SetServerReportedRegions(host, mockRegions)
		e.serverDirectedReconnect.Store(serverDirected)
		return e
	}
	down := errors.New("region down")

	t.Run("server-directed skips current and falls back past a failed region", func(t *testing.T) {
		e := newEngine(true)
		var dialed []string
		err := e.connectWithRegionFailover(func(url string) error {
			dialed = append(dialed, url)
			if url == "wss://region-b" {
				return down // initial region fails
			}
			return nil // region-c succeeds
		})
		require.NoError(t, err)
		require.Equal(t, []string{"wss://region-b", "wss://region-c"}, dialed)
		require.NotContains(t, dialed, "wss://region-a", "current region must be skipped when server-directed")
	})

	t.Run("not server-directed tries current first then falls back", func(t *testing.T) {
		e := newEngine(false)
		var dialed []string
		err := e.connectWithRegionFailover(func(url string) error {
			dialed = append(dialed, url)
			if url == "wss://region-a" {
				return down // current fails
			}
			return nil // region-b succeeds
		})
		require.NoError(t, err)
		require.Equal(t, []string{"wss://region-a", "wss://region-b"}, dialed)
	})

	t.Run("all candidates fail returns the last error", func(t *testing.T) {
		e := newEngine(true)
		var dialed []string
		err := e.connectWithRegionFailover(func(url string) error {
			dialed = append(dialed, url)
			return down
		})
		require.ErrorIs(t, err, down)
		require.Equal(t, []string{"wss://region-b", "wss://region-c", "wss://global"}, dialed)
	})

	t.Run("falls back to the global url when all regions fail", func(t *testing.T) {
		e := newEngine(true)
		var dialed []string
		err := e.connectWithRegionFailover(func(url string) error {
			dialed = append(dialed, url)
			if url == "wss://global" {
				return nil // only the global url is reachable
			}
			return down
		})
		require.NoError(t, err)
		require.Equal(t, []string{"wss://region-b", "wss://region-c", "wss://global"}, dialed)
	})

	t.Run("non-cloud dials only the original url", func(t *testing.T) {
		// no region provider / cloud host -> the region branch is skipped entirely
		e := &RTCEngine{
			log:         logger,
			url:         "wss://current",
			originalURL: "wss://global",
		}
		e.token.Store("tok")
		var dialed []string
		err := e.connectWithRegionFailover(func(url string) error {
			dialed = append(dialed, url)
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, []string{"wss://global"}, dialed)
	})

	t.Run("server-directed flag is consumed per sweep", func(t *testing.T) {
		e := newEngine(true)
		failAll := func(dialed *[]string) func(string) error {
			return func(url string) error {
				*dialed = append(*dialed, url)
				return down
			}
		}
		var sweep1, sweep2 []string
		_ = e.connectWithRegionFailover(failAll(&sweep1))
		_ = e.connectWithRegionFailover(failAll(&sweep2))

		require.NotContains(t, sweep1, "wss://region-a", "first sweep is server-directed: current is skipped")
		require.Equal(t, "wss://region-a", sweep2[0], "second sweep reverts to current-first once the flag is consumed")
	})

	t.Run("degrades to the global url when region settings are unavailable", func(t *testing.T) {
		rs := newRegionSettingsServer(t, &livekit.RegionSettings{})
		rs.mu.Lock()
		rs.status = http.StatusServiceUnavailable
		rs.mu.Unlock()
		e := &RTCEngine{
			log:            logger,
			regionProvider: newTestProvider(rs),
			cloudHost:      rs.hostname,
			url:            "wss://current",
			originalURL:    "wss://global",
		}
		e.token.Store("test-token") // the fake settings server asserts this exact bearer token
		e.serverDirectedReconnect.Store(true)
		var dialed []string
		err := e.connectWithRegionFailover(func(url string) error {
			dialed = append(dialed, url)
			return nil
		})
		require.NoError(t, err)
		// no region list available and current skipped (server-directed) -> only global
		require.Equal(t, []string{"wss://global"}, dialed)
	})
}
