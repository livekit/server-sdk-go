package lksdk

import (
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
)

// regionSettingsURL builds the LiveKit Cloud region-discovery URL for a hostname.
var regionSettingsURL = func(cloudHostname string) string {
	return "https://" + cloudHostname + "/settings/regions"
}

type regionURLProvider struct {
	hostnameSettingsCache map[string]*hostnameSettingsCacheItem // hostname -> regionSettings

	mutex      sync.RWMutex
	httpClient *http.Client
}

type hostnameSettingsCacheItem struct {
	regionSettings *livekit.RegionSettings
	updatedAt      time.Time
	cacheTTL       time.Duration // from Cache-Control max-age; falls back to the default
}

func newRegionURLProvider() *regionURLProvider {
	return &regionURLProvider{
		hostnameSettingsCache: make(map[string]*hostnameSettingsCacheItem),
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (r *regionURLProvider) RefreshRegionSettings(cloudHostname, token string) error {
	r.mutex.RLock()
	hostnameSettings := r.hostnameSettingsCache[cloudHostname]
	r.mutex.RUnlock()

	if hostnameSettings != nil {
		ttl := hostnameSettings.cacheTTL
		if ttl <= 0 {
			ttl = regionHostnameProviderSettingsCacheTime
		}
		if time.Since(hostnameSettings.updatedAt) < ttl {
			return nil
		}
	}

	settingsURL := regionSettingsURL(cloudHostname)
	req, err := http.NewRequest("GET", settingsURL, nil)
	if err != nil {
		return errors.New("refreshRegionSettings failed to create request: " + err.Error())
	}
	req.Header = http.Header{
		"Authorization": []string{"Bearer " + token},
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("refreshRegionSettings failed to fetch region settings. http status: " + resp.Status)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.New("refreshRegionSettings failed to read response body: " + err.Error())
	}
	regions := &livekit.RegionSettings{}
	if err := protojson.Unmarshal(respBody, regions); err != nil {
		return errors.New("refreshRegionSettings failed to decode region settings: " + err.Error())
	}

	ttl := regionHostnameProviderSettingsCacheTime
	if maxAge := parseRegionSettingsMaxAge(resp.Header.Get("Cache-Control")); maxAge > 0 {
		ttl = maxAge
	}

	r.mutex.Lock()
	item := &hostnameSettingsCacheItem{
		regionSettings: regions,
		updatedAt:      time.Now(),
		cacheTTL:       ttl,
	}
	r.hostnameSettingsCache[cloudHostname] = item
	r.mutex.Unlock()

	if len(item.regionSettings.Regions) == 0 {
		logger.Warnw("no regions returned", nil, "cloudHostname", cloudHostname)
	}

	return nil
}

// RegionSettings returns the cached region list for a hostname, refreshing it if
// stale. The returned list is owned by the cache and must not be mutated; the
// caller is responsible for tracking its own per-failover attempt state.
func (r *regionURLProvider) RegionSettings(cloudHostname, token string) (*livekit.RegionSettings, error) {
	if err := r.RefreshRegionSettings(cloudHostname, token); err != nil {
		r.mutex.RLock()
		cached := r.hostnameSettingsCache[cloudHostname]
		r.mutex.RUnlock()
		if cached == nil {
			return nil, err
		}
		// a cached list exists; fall through and use it
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()
	item := r.hostnameSettingsCache[cloudHostname]
	if item == nil {
		return nil, errors.New("no regions available")
	}
	return item.regionSettings, nil
}

// SetServerReportedRegions stores a region list pushed by the server (e.g. on a
// reconnect LeaveRequest), overriding the cached /settings/regions list and
// resetting the cache TTL so the next failover uses it without re-fetching.
func (r *regionURLProvider) SetServerReportedRegions(cloudHostname string, regions *livekit.RegionSettings) {
	if regions == nil {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	ttl := regionHostnameProviderSettingsCacheTime
	if existing := r.hostnameSettingsCache[cloudHostname]; existing != nil && existing.cacheTTL > 0 {
		ttl = existing.cacheTTL
	}
	r.hostnameSettingsCache[cloudHostname] = &hostnameSettingsCacheItem{
		regionSettings: regions,
		updatedAt:      time.Now(),
		cacheTTL:       ttl,
	}
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
