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
	defer r.mutex.RUnlock()

	return r.refreshRegionSettings(cloudHostname, token)
}

// BestURL returns the region with least number of attempts, in the order provided by the region settings endpoint (round-robin)
func (r *regionURLProvider) BestURL(cloudHostname, token string) (string, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	hostnameSettings := r.hostnameSettingsCache[cloudHostname]
	if hostnameSettings == nil || time.Since(hostnameSettings.updatedAt) > regionHostnameProviderSettingsCacheTime {
		if err := r.refreshRegionSettings(cloudHostname, token); err != nil {
			return "", fmt.Errorf("BestURL could not refresh region settings: %v", err)
		}
		hostnameSettings = r.hostnameSettingsCache[cloudHostname]
	}

	if hostnameSettings == nil || hostnameSettings.regionSettings == nil || len(hostnameSettings.regionSettings.Regions) == 0 {
		return "", errors.New("no regions available")
	}

	var bestRegionURL string
	minAttempts := -1
	for _, region := range hostnameSettings.regionSettings.Regions {
		if minAttempts == -1 || hostnameSettings.regionURLAttempts[region.Url] < minAttempts {
			minAttempts = hostnameSettings.regionURLAttempts[region.Url]
			bestRegionURL = region.Url
		}
	}
	hostnameSettings.regionURLAttempts[bestRegionURL]++

	return bestRegionURL, nil
}

// ReportAttemptFailure reports a failed attempt to connect to a region, so that it can be avoided in future connection attempts
func (r *regionURLProvider) ReportAttemptFailure(cloudHostname, regionURL string) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	hostnameSettings := r.hostnameSettingsCache[cloudHostname]
	if hostnameSettings == nil {
		return errors.New("ReportAttemptFailure: serverURL not found in cache")
	}

	// remove failed region from regionSettings
	for i, region := range hostnameSettings.regionSettings.Regions {
		if region.Url == regionURL {
			hostnameSettings.regionSettings.Regions = append(
				hostnameSettings.regionSettings.Regions[:i],
				hostnameSettings.regionSettings.Regions[i+1:]...,
			)
			break
		}
	}

	return nil
}

// assume this is being called within a lock
func (r *regionURLProvider) refreshRegionSettings(cloudHostname, token string) error {
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

	r.hostnameSettingsCache[cloudHostname] = &hostnameSettingsCacheItem{
		regionSettings:    regions,
		updatedAt:         time.Now(),
		regionURLAttempts: map[string]int{},
	}

	return nil
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
