package lksdk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
	regionSettings       *livekit.RegionSettings
	updatedAt            time.Time
	successfulRegionURLs map[string]int
}

func (r *regionURLProvider) RefreshRegionSettings(serverURL, token string) error {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return err
	}

	parsedHostname := parsedURL.Hostname()
	if !isCloud(parsedHostname) {
		return nil
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return r.refreshRegionSettings(parsedHostname, token)
}

func (r *regionURLProvider) BestURL(serverURL, token string) (string, error) {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}

	parsedHostname := parsedURL.Hostname()
	if !isCloud(parsedHostname) {
		return serverURL, nil
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	hostnameSettings := r.hostnameSettingsCache[parsedHostname]
	if hostnameSettings == nil || time.Since(hostnameSettings.updatedAt) > regionHostnameProviderSettingsCacheTime {
		if err := r.refreshRegionSettings(parsedHostname, token); err != nil {
			return "", err
		}
		hostnameSettings = r.hostnameSettingsCache[parsedHostname]
	}

	if hostnameSettings == nil || hostnameSettings.regionSettings == nil || len(hostnameSettings.regionSettings.Regions) == 0 {
		return "", errors.New("no regions available")
	}

	// return the first region with least number of successful attempts
	var bestRegionURL string
	minAttempts := -1
	for _, region := range hostnameSettings.regionSettings.Regions {
		parsedRegionURL, err := url.Parse(region.Url)
		if err != nil {
			continue
		}
		parsedRegionHostname := parsedRegionURL.Hostname()
		if minAttempts == -1 || hostnameSettings.successfulRegionURLs[parsedRegionHostname] < minAttempts {
			minAttempts = hostnameSettings.successfulRegionURLs[parsedRegionHostname]
			bestRegionURL = region.Url
		}
	}

	return bestRegionURL, nil
}

func (r *regionURLProvider) ReportAttempt(serverURL, regionURL string, success bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	hostnameSettings := r.hostnameSettingsCache[serverURL]
	if hostnameSettings == nil {
		return
	}

	if success {
		hostnameSettings.successfulRegionURLs[regionURL]++
	} else {
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
	}
}

// assume this is being called within a lock
func (r *regionURLProvider) refreshRegionSettings(settingsHostname, token string) error {
	if r.httpClient == nil {
		r.httpClient = &http.Client{
			Timeout: 5 * time.Second,
		}
	}

	settingsURL := "https://" + settingsHostname + "/regions"
	req, err := http.NewRequest("GET", settingsURL, nil)
	if err != nil {
		return err
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
		return errors.New("failed to fetch region settings. http status: " + resp.Status)
	}

	var settings *livekit.RegionSettings
	if err := json.NewDecoder(resp.Body).Decode(settings); err != nil {
		return err
	}

	r.hostnameSettingsCache[settingsHostname] = &hostnameSettingsCacheItem{
		regionSettings:       settings,
		updatedAt:            time.Now(),
		successfulRegionURLs: map[string]int{},
	}

	return nil
}

func isCloud(hostname string) bool {
	return strings.HasSuffix(hostname, "livekit.cloud") || strings.HasSuffix(hostname, "livekit.io")
}
