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
	"time"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/server-sdk-go/v2/signalling"
)

// Total attempts (the original request plus fallback regions) and the base
// retry backoff are fixed, not user-configurable, so retries can't be tuned to
// values that could overwhelm the server.
const (
	failoverMaxAttempts = 3
	failoverBackoffBase = 200 * time.Millisecond
)

// failoverConfig is the resolved per-request region-failover configuration. The
// public API exposes only the enabled toggle (default true); force and
// backoffBase are internal test-only knobs.
type failoverConfig struct {
	enabled bool
	// force bypasses the cloud-host check. Internal testing only.
	force bool
	// backoffBase overrides the retry backoff base. Internal testing only.
	backoffBase time.Duration
}

// attempts returns the total request attempts for a host; 1 means no failover.
// Failover only engages when enabled and the host is a LiveKit Cloud domain.
// force bypasses the cloud-host check and is for internal testing only.
func (c failoverConfig) attempts(hostname string) int {
	if c.enabled && (c.force || isCloud(hostname)) {
		return failoverMaxAttempts
	}
	return 1
}

type failoverEnabledKey struct{}
type failoverForceKey struct{}

// WithFailover returns a context that enables or disables region failover for
// API requests made with it (enabled by default). Failover only engages for
// LiveKit Cloud hosts. Pass the returned context to any service client method.
func WithFailover(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, failoverEnabledKey{}, enabled)
}

// withFailoverForce returns a context that forces failover on regardless of
// host and overrides the retry backoff. Internal, test-only.
func withFailoverForce(ctx context.Context, backoff time.Duration) context.Context {
	return context.WithValue(ctx, failoverForceKey{}, backoff)
}

func failoverConfigFromContext(ctx context.Context) failoverConfig {
	cfg := failoverConfig{enabled: true, backoffBase: failoverBackoffBase}
	if enabled, ok := ctx.Value(failoverEnabledKey{}).(bool); ok {
		cfg.enabled = enabled
	}
	if backoff, ok := ctx.Value(failoverForceKey{}).(time.Duration); ok {
		cfg.force = true
		cfg.backoffBase = backoff
	}
	return cfg
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
	regions *regionCache
}

func (t *failoverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cfg := failoverConfigFromContext(req.Context())
	maxAttempts := cfg.attempts(req.URL.Hostname())

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

		// discover regions lazily; honor the request scheme (so it works against
		// an http mock) and forward the caller's headers to the discovery fetch.
		if !fetchedRegions {
			discoveryURL := url.URL{Scheme: originalScheme, Host: originalHost, Path: "/settings/regions"}
			settings, _ = t.regions.get(originalHost, discoveryURL.String(), req.Header, 0)
			fetchedRegions = true
		}
		nextScheme, nextHost, ok := nextRegion(settings, attempted)
		if !ok {
			break // no untried region left
		}

		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		logger.Warnw("livekit API request failed, retrying with fallback url", err,
			"failedUrl", scheme+"://"+host, "fallbackUrl", nextScheme+"://"+nextHost,
			"attempt", attempt+1, "maxAttempts", maxAttempts, "status", status)

		drainResponse(resp)
		if !sleepCtx(req.Context(), cfg.backoffBase<<uint(attempt)) {
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

// sharedAPIRegions is the process-wide region cache used by the API failover
// path. The RTC signaling path has its own regionURLProvider over a separate
// cache, but both share the same regionCache discovery/fetch/TTL logic.
var sharedAPIRegions = newRegionCache()

// TwirpRegionError indicates the /settings/regions endpoint returned a non-200
// status while attempting region failover.
type TwirpRegionError struct {
	StatusCode int
}

func (e *TwirpRegionError) Error() string {
	return "failed to fetch region settings: status " + http.StatusText(e.StatusCode)
}
