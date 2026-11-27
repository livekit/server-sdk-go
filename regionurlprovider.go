package lksdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/protocol/livekit"
)

const (
	regionHostnameProviderSettingsCacheTime = 3 * time.Second
	// rtcRegionDiscoveryTimeout bounds a /settings/regions fetch before an RTC join.
	// It exceeds the common 2s DNS forwarder retry so one slow lookup does not
	// fail discovery.
	rtcRegionDiscoveryTimeout = 3 * time.Second
	// apiRegionDiscoveryTimeout bounds a /settings/regions fetch during API
	// failover. It stays short because failover runs while the caller's request
	// waits; the fetch also stops when the caller cancels the request.
	apiRegionDiscoveryTimeout = 2 * time.Second
)

// regionSettingsURL builds the LiveKit Cloud region-discovery URL for a hostname.
var regionSettingsURL = func(cloudHostname string) string {
	return "https://" + cloudHostname + "/settings/regions"
}

// regionURLProvider supplies LiveKit Cloud region lists to the RTC signaling
// reconnect path. It is a thin token/https adapter over the shared regionCache,
// adding a default cache TTL and support for server-pushed region lists.
type regionURLProvider struct {
	cache *regionCache
}

func newRegionURLProvider() *regionURLProvider {
	return &regionURLProvider{cache: newRegionCache(rtcRegionDiscoveryTimeout)}
}

func (r *regionURLProvider) RefreshRegionSettings(ctx context.Context, cloudHostname, token string) error {
	_, err := r.RegionSettings(ctx, cloudHostname, token)
	return err
}

// RegionSettings returns the cached region list for a hostname, refreshing it if
// stale. A refresh stops when ctx is done. The returned list is owned by the
// cache and must not be mutated; the caller is responsible for tracking its own
// per-failover attempt state.
func (r *regionURLProvider) RegionSettings(ctx context.Context, cloudHostname, token string) (*livekit.RegionSettings, error) {
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	settings, err := r.cache.get(ctx, cloudHostname, regionSettingsURL(cloudHostname), headers, regionHostnameProviderSettingsCacheTime)
	if err != nil {
		return nil, err
	}
	if settings == nil {
		return nil, errors.New("no regions available")
	}
	if len(settings.Regions) == 0 {
		logger.Warnw("no regions returned", nil, "cloudHostname", cloudHostname)
	}
	return settings, nil
}

// regionCache fetches and caches the LiveKit Cloud region list per host. It is
// shared by the API failover path (which honors the request scheme and forwards
// the caller's headers) and the RTC signaling path (which fetches over https
// with a bearer token). The caller supplies the discovery URL so each path
// keeps its own URL scheme and test seams.
type regionCache struct {
	mu     sync.Mutex
	client *http.Client
	cache  map[string]*regionCacheEntry
}

type regionCacheEntry struct {
	settings  *livekit.RegionSettings
	fetchedAt time.Time
	ttl       time.Duration
}

// newRegionCache returns a cache whose fetches are bounded by timeout.
func newRegionCache(timeout time.Duration) *regionCache {
	return &regionCache{
		client: &http.Client{Timeout: timeout},
		cache:  make(map[string]*regionCacheEntry),
	}
}

// get returns the cached region list for key, fetching discoveryURL if the
// cache is stale. The server's Cache-Control max-age sets the TTL; when absent,
// defaultTTL is used (0 means "do not cache"). A fetch stops when ctx is done or
// the cache's timeout elapses, whichever comes first. Best-effort: on a fetch
// failure it serves a stale cached list when available, otherwise returns the error.
func (c *regionCache) get(ctx context.Context, key, discoveryURL string, headers http.Header, defaultTTL time.Duration) (*livekit.RegionSettings, error) {
	key = strings.ToLower(key)

	c.mu.Lock()
	if entry := c.cache[key]; entry != nil && time.Since(entry.fetchedAt) < entry.ttl {
		defer c.mu.Unlock()
		return entry.settings, nil
	}
	c.mu.Unlock()

	settings, ttl, err := c.fetch(ctx, discoveryURL, headers)
	if err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if entry := c.cache[key]; entry != nil {
			return entry.settings, nil // serve stale on failure
		}
		return nil, err
	}

	if ttl <= 0 {
		ttl = defaultTTL
	}
	if ttl > 0 {
		c.mu.Lock()
		c.cache[key] = &regionCacheEntry{settings: settings, fetchedAt: time.Now(), ttl: ttl}
		c.mu.Unlock()
	}
	return settings, nil
}

// set stores a region list pushed out-of-band (e.g. by the server on reconnect),
// overriding any cached list and keeping the existing TTL, or defaultTTL.
func (c *regionCache) set(key string, settings *livekit.RegionSettings, defaultTTL time.Duration) {
	if settings == nil {
		return
	}
	key = strings.ToLower(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := defaultTTL
	if existing := c.cache[key]; existing != nil && existing.ttl > 0 {
		ttl = existing.ttl
	}
	c.cache[key] = &regionCacheEntry{settings: settings, fetchedAt: time.Now(), ttl: ttl}
}

func (c *regionCache) fetch(ctx context.Context, discoveryURL string, headers http.Header) (*livekit.RegionSettings, time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, 0, err
	}
	// Forward the caller's headers (Authorization and any custom headers),
	// minus body-specific ones, so a validly-signed token reaches the discovery
	// endpoint and test directives propagate.
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
		return nil, 0, &RegionError{StatusCode: resp.StatusCode}
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

// parseRegionSettingsMaxAge extracts the max-age (seconds) from a Cache-Control
// header value, returning 0 when absent or unparseable. Directive names are
// case-insensitive per RFC 9111.
func parseRegionSettingsMaxAge(cacheControl string) time.Duration {
	for _, directive := range strings.Split(cacheControl, ",") {
		directive = strings.ToLower(strings.TrimSpace(directive))
		if strings.HasPrefix(directive, "max-age=") {
			if secs, err := strconv.Atoi(strings.TrimPrefix(directive, "max-age=")); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return 0
}

func parseCloudURL(serverURL string) (string, error) {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("invalid server url (%s): %v", serverURL, err)
	}

	if !isCloud(parsedURL.Hostname()) {
		return "", errors.New("not a cloud url")
	}

	return parsedURL.Hostname(), nil
}

// isCloud reports whether the hostname belongs to a LiveKit Cloud project
// (a *.livekit.cloud subdomain).
var isCloud = func(hostname string) bool {
	return strings.HasSuffix(hostname, ".livekit.cloud")
}
