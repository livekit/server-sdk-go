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
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/server-sdk-go/v2/signalling"
)

// FailoverMode controls when API requests fail over to alternative LiveKit
// Cloud regions on a retryable error.
type FailoverMode int

const (
	// FailoverAuto enables region failover only for LiveKit Cloud hosts. This
	// is the default.
	FailoverAuto FailoverMode = iota
	// FailoverOn forces region failover on regardless of host. Primarily for
	// tests pointing at a non-cloud host (e.g. a local mock server).
	FailoverOn
	// FailoverOff disables region failover entirely.
	FailoverOff
)

const (
	defaultMaxAttempts = 3
	defaultBackoffBase = 200 * time.Millisecond
)

// FailoverConfig tunes the region-failover retry loop. A zero value is valid
// and uses the defaults (auto mode, 3 attempts, 200ms base backoff).
type FailoverConfig struct {
	// Mode selects when failover is active. Defaults to FailoverAuto.
	Mode FailoverMode
	// MaxAttempts is the total number of attempts including the first, so the
	// original host plus (MaxAttempts-1) fallback regions. Defaults to 3.
	MaxAttempts int
	// BackoffBase is the initial delay before the first retry; each subsequent
	// retry doubles it. Defaults to 200ms. Set to 0 to retry without delay.
	BackoffBase time.Duration
}

func (c FailoverConfig) withDefaults() FailoverConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaultMaxAttempts
	}
	if c.BackoffBase < 0 {
		c.BackoffBase = 0
	} else if c.BackoffBase == 0 {
		c.BackoffBase = defaultBackoffBase
	}
	return c
}

func (c FailoverConfig) enabledFor(hostname string) bool {
	switch c.Mode {
	case FailoverOff:
		return false
	case FailoverOn:
		return true
	default:
		return isCloud(hostname)
	}
}

type failoverConfigKey struct{}

// WithFailoverConfig returns a context that overrides the region-failover
// behavior for API requests made with it. Pass the returned context to any
// service client method.
func WithFailoverConfig(ctx context.Context, cfg FailoverConfig) context.Context {
	return context.WithValue(ctx, failoverConfigKey{}, cfg)
}

func failoverConfigFromContext(ctx context.Context) FailoverConfig {
	if cfg, ok := ctx.Value(failoverConfigKey{}).(FailoverConfig); ok {
		return cfg.withDefaults()
	}
	return FailoverConfig{}.withDefaults()
}

// newAPIHTTPClient returns the *http.Client used by every API service client.
// It wraps the default transport with region-failover retries.
func newAPIHTTPClient() *http.Client {
	return &http.Client{
		Transport: &failoverTransport{base: http.DefaultTransport, regions: sharedAPIRegions},
	}
}

// failoverTransport orchestrates region failover for a single API request. On
// a retryable failure (any transport error or HTTP 5xx) it discovers
// alternative regions via /settings/regions and replays the request — body and
// headers intact — against the next region, with exponential backoff. 4xx
// responses are returned immediately.
type failoverTransport struct {
	base    http.RoundTripper
	regions *apiRegionCache
}

func (t *failoverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cfg := failoverConfigFromContext(req.Context())
	enabled := cfg.enabledFor(req.URL.Hostname())

	// Buffer the body once so it can be replayed against each region.
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}

	originalScheme, originalHost := req.URL.Scheme, req.URL.Host
	attempted := map[string]struct{}{strings.ToLower(originalHost): {}}

	maxAttempts := cfg.MaxAttempts
	if !enabled {
		maxAttempts = 1
	}

	var (
		settings       *livekit.RegionSettings
		fetchedRegions bool
		resp           *http.Response
		err            error
	)
	scheme, host := originalScheme, originalHost

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}

		r := req.Clone(req.Context())
		r.URL.Scheme = scheme
		r.URL.Host = host
		r.Host = host
		if body != nil {
			buf := body
			r.Body = io.NopCloser(bytes.NewReader(buf))
			r.ContentLength = int64(len(buf))
			r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf)), nil }
		}

		resp, err = t.base.RoundTrip(r)
		if !isRetryable(resp, err) {
			return resp, err
		}
		if attempt == maxAttempts-1 {
			break // out of attempts; surface the last result
		}

		// discover regions lazily
		if !fetchedRegions {
			settings = t.regions.get(originalScheme, originalHost, req.Header)
			fetchedRegions = true
		}
		nextScheme, nextHost, ok := nextRegion(settings, attempted)
		if !ok {
			break // no untried region left
		}

		drainResponse(resp)
		if !sleepCtx(req.Context(), cfg.BackoffBase<<uint(attempt)) {
			return resp, err // context cancelled during backoff
		}
		scheme, host = nextScheme, nextHost
		attempted[strings.ToLower(host)] = struct{}{}
	}
	return resp, err
}

// isRetryable reports whether a region failover should be attempted: any
// transport error, or an HTTP 5xx response. 4xx and success are terminal.
func isRetryable(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return resp != nil && resp.StatusCode >= 500
}

// nextRegion returns the first region whose host has not yet been attempted.
func nextRegion(settings *livekit.RegionSettings, attempted map[string]struct{}) (scheme, host string, ok bool) {
	if settings == nil {
		return "", "", false
	}
	for _, region := range settings.Regions {
		u, err := url.Parse(signalling.ToHttpURL(region.Url))
		if err != nil || u.Host == "" {
			continue
		}
		if _, seen := attempted[strings.ToLower(u.Host)]; seen {
			continue
		}
		return u.Scheme, u.Host, true
	}
	return "", "", false
}

func drainResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var sharedAPIRegions = newAPIRegionCache()

// apiRegionCache fetches and caches the LiveKit Cloud region list per host for
// the API failover path. Unlike the signaling regionURLProvider it honors the
// request scheme (so it works against an http mock server) and forwards the
// caller's headers (auth plus any custom headers) to the discovery endpoint.
type apiRegionCache struct {
	mu     sync.Mutex
	client *http.Client
	cache  map[string]*apiRegionEntry
}

type apiRegionEntry struct {
	settings  *livekit.RegionSettings
	fetchedAt time.Time
	ttl       time.Duration
}

func newAPIRegionCache() *apiRegionCache {
	return &apiRegionCache{
		client: &http.Client{Timeout: 5 * time.Second},
		cache:  make(map[string]*apiRegionEntry),
	}
}

// get returns the region list for host, fetching it if the cache is stale.
// It is best-effort: on a fetch failure it falls back to a stale cached list,
// and returns nil if nothing is available (the caller then stops failing over).
func (c *apiRegionCache) get(scheme, host string, headers http.Header) *livekit.RegionSettings {
	key := strings.ToLower(host)

	c.mu.Lock()
	if entry := c.cache[key]; entry != nil && time.Since(entry.fetchedAt) < entry.ttl {
		defer c.mu.Unlock()
		return entry.settings
	}
	c.mu.Unlock()

	settings, ttl, err := c.fetch(scheme, host, headers)
	if err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if entry := c.cache[key]; entry != nil {
			return entry.settings // serve stale on failure
		}
		return nil
	}

	// A zero TTL (e.g. Cache-Control: max-age=0) means "do not cache".
	if ttl > 0 {
		c.mu.Lock()
		c.cache[key] = &apiRegionEntry{settings: settings, fetchedAt: time.Now(), ttl: ttl}
		c.mu.Unlock()
	}
	return settings
}

func (c *apiRegionCache) fetch(scheme, host string, headers http.Header) (*livekit.RegionSettings, time.Duration, error) {
	u := url.URL{Scheme: scheme, Host: host, Path: "/settings/regions"}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	// Forward the caller's headers (Authorization and any custom headers),
	// minus body-specific ones, so a validly-signed token reaches the
	// discovery endpoint and test directives propagate.
	for k, vv := range headers {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Type", "Content-Length":
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer drainResponse(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, 0, &TwirpRegionError{StatusCode: resp.StatusCode}
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	settings := &livekit.RegionSettings{}
	if err := protojson.Unmarshal(b, settings); err != nil {
		return nil, 0, err
	}
	ttl := parseRegionSettingsMaxAge(resp.Header.Get("Cache-Control"))
	return settings, ttl, nil
}

// TwirpRegionError indicates the /settings/regions endpoint returned a non-200
// status while attempting region failover.
type TwirpRegionError struct {
	StatusCode int
}

func (e *TwirpRegionError) Error() string {
	return "failed to fetch region settings: status " + http.StatusText(e.StatusCode)
}
