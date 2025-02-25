package lksdk

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/protocol/livekit"
)

const (
	regionHostnameProviderSettingsCacheTime = 3 * time.Second
)

type regionURLProvider struct {
	hostnameSettingsCache map[string]*hostnameSettingsCacheItem // hostname -> regionSettings

	mutex      sync.RWMutex
	httpClient *http.Client
}

type hostnameSettingsCacheItem struct {
	regionSettings    *livekit.RegionSettings
	updatedAt         time.Time
	regionURLAttempts map[string]int
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

	if hostnameSettings != nil && time.Since(hostnameSettings.updatedAt) < regionHostnameProviderSettingsCacheTime {
		return nil
	}

	settingsURL := "https://" + cloudHostname + "/settings/regions"
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

	item := &hostnameSettingsCacheItem{
		regionSettings:    regions,
		updatedAt:         time.Now(),
		regionURLAttempts: map[string]int{},
	}
	r.mutex.Lock()
	r.hostnameSettingsCache[cloudHostname] = item
	r.mutex.Unlock()

	if len(item.regionSettings.Regions) == 0 {
		logger.Warnw("no regions returned", nil, "cloudHostname", cloudHostname)
	}

	return nil
}

// PopBestURL removes and returns the best region URL. Once all URLs are exhausted, it will return an error.
// RefreshRegionSettings must be called to repopulate the list of regions.
func (r *regionURLProvider) PopBestURL(cloudHostname, token string) (string, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	hostnameSettings := r.hostnameSettingsCache[cloudHostname]

	if hostnameSettings == nil || hostnameSettings.regionSettings == nil || len(hostnameSettings.regionSettings.Regions) == 0 {
		return "", errors.New("no regions available")
	}

	bestRegionURL := hostnameSettings.regionSettings.Regions[0].Url
	hostnameSettings.regionSettings.Regions = hostnameSettings.regionSettings.Regions[1:]

	return bestRegionURL, nil
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

func isCloud(hostname string) bool {
	return strings.HasSuffix(hostname, "livekit.cloud") || strings.HasSuffix(hostname, "livekit.io")
}
