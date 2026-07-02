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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
	"go.uber.org/zap/zapcore"

	"github.com/livekit/server-sdk-go/v2/signalling"
)

const (
	cConnectTimeoutDefault = 15 * time.Second
	cValidateTimeout       = 3 * time.Second
)

// -------------------------------------------

type connectionManagerState int

const (
	connectionManagerStateInitial connectionManagerState = iota
	connectionManagerStateConnected
	connectionManagerStateResuming
	connectionManagerStateReconnecting
	connectionManagerStateDisconnected
)

func (c connectionManagerState) String() string {
	switch c {
	case connectionManagerStateInitial:
		return "INITIAL"
	case connectionManagerStateConnected:
		return "CONNECTED"
	case connectionManagerStateResuming:
		return "RESUMING"
	case connectionManagerStateReconnecting:
		return "RECONNECTING"
	case connectionManagerStateDisconnected:
		return "DISCONNECTED"
	default:
		return fmt.Sprintf("UNKNOWN (%d)", c)
	}
}

// -------------------------------------------

type connectionRequestParams struct {
	ctx                    context.Context
	url                    string
	token                  string
	connectParams          signalling.ConnectParams
	disableRegionDiscovery bool
}

// -------------------------------------------

type connectionAttemptParams struct {
	ctx             context.Context
	backoffWait     time.Duration
	region          *livekit.RegionInfo
	token           string
	validateTimeout time.Duration
}

func (c connectionAttemptParams) MarshalLogObject(e zapcore.ObjectEncoder) error {
	deadline, hasDeadline := c.ctx.Deadline()
	if hasDeadline {
		e.AddDuration("ctxDeadline", time.Until(deadline))
	} else {
		e.AddString("ctxDeadline", "not-set")
	}
	e.AddDuration("backoffWait", c.backoffWait)
	e.AddObject("region", protoLogger.Proto(c.region))
	e.AddDuration("validateTimeout", c.validateTimeout)
	return nil
}

// -------------------------------------------

type connectionManager struct {
	mu sync.RWMutex

	log            protoLogger.Logger
	regionProvider *regionURLProvider

	incomingRequestParams connectionRequestParams

	token string

	connectedRegion *livekit.RegionInfo

	regionSettings *livekit.RegionSettings

	state connectionManagerState
}

func newConnectionManager(regionProvider *regionURLProvider) *connectionManager {
	return &connectionManager{
		log:            logger,
		regionProvider: regionProvider,
		state:          connectionManagerStateInitial,
	}
}

func (c *connectionManager) setLogger(l protoLogger.Logger) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.log = l
}

func (c *connectionManager) setIncomingRequestParams(
	ctx context.Context,
	url string,
	token string,
	connectParams *signalling.ConnectParams,
	disableRegionDiscovery bool,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.incomingRequestParams = connectionRequestParams{
		ctx:                    ctx,
		url:                    url,
		token:                  token,
		connectParams:          *connectParams,
		disableRegionDiscovery: disableRegionDiscovery,
	}
	c.token = token
}

func (c *connectionManager) getConnectParams() signalling.ConnectParams {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.incomingRequestParams.connectParams
}

func (c *connectionManager) getConnectTimeout() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.incomingRequestParams.connectParams.ConnectTimeout <= 0 {
		return cConnectTimeoutDefault
	}

	return c.incomingRequestParams.connectParams.ConnectTimeout
}

func (c *connectionManager) setToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.token = token
}

func (c *connectionManager) setConnected(region *livekit.RegionInfo) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// reset on connection establishment to ensure region settings in leave request from a
	// previously connected server is not used past its validity,
	//
	// if a resume/reconnect is needed after this, region settings from the newly connected
	// server will be used if the new server provides one in the leave request
	c.regionSettings = nil
	c.connectedRegion = utils.CloneProto(region)
	c.updateState(connectionManagerStateConnected)
	return true
}

// setResumed restores the Connected state after a successful resume so the next
// resume starts fresh. It is a no-op unless still Resuming: if a reconnect was
// requested while the resume was in progress, the state is left Reconnecting so
// the pending full reconnect proceeds rather than being clobbered.
func (c *connectionManager) setResumed(region *livekit.RegionInfo) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state != connectionManagerStateResuming {
		return false
	}

	c.regionSettings = nil
	c.connectedRegion = utils.CloneProto(region)
	c.updateState(connectionManagerStateConnected)
	return true
}

func (c *connectionManager) setResuming(regionSettigs *livekit.RegionSettings) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// if already reconnecting, resuming is a no-op till the reconnect finishes
	if c.state == connectionManagerStateReconnecting {
		return false
	}

	// if not connected, cannot resume, so no-op
	if c.state != connectionManagerStateConnected {
		return false
	}

	// if already resuming, do not take settings that are nil as some internal paths might do a resume without regions
	if c.state == connectionManagerStateResuming {
		if regionSettigs != nil {
			c.regionSettings = utils.CloneProto(regionSettigs)
		}
		return false
	}

	c.regionSettings = utils.CloneProto(regionSettigs)
	c.updateState(connectionManagerStateResuming)
	return true
}

func (c *connectionManager) setReconnecting(regionSettigs *livekit.RegionSettings) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.regionSettings = utils.CloneProto(regionSettigs)
	c.updateState(connectionManagerStateReconnecting)
	return true
}

func (c *connectionManager) isReconnectingState() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.state == connectionManagerStateReconnecting
}

func (c *connectionManager) setDisconnected() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.regionSettings = nil
	c.updateState(connectionManagerStateDisconnected)
}

func (c *connectionManager) updateState(state connectionManagerState) {
	if c.state == state {
		return
	}

	c.log.Infow(
		"connection manager state change",
		"old", c.state,
		"new", state,
		"regionSettings", protoLogger.Proto(c.regionSettings),
		"currentRegion", protoLogger.Proto(c.connectedRegion),
	)
	c.state = state
}

func (c *connectionManager) getConnectionPlan() ([]connectionAttemptParams, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.incomingRequestParams.url == "" {
		return nil, errors.New("original url not set")
	}

	switch c.state {
	case connectionManagerStateInitial:
		return c.getConnectionPlanInitial()
	case connectionManagerStateResuming:
		return c.getConnectionPlanResuming()
	case connectionManagerStateReconnecting:
		return c.getConnectionPlanReconnecting()
	}

	return nil, errors.New("invalid state")
}

func (c *connectionManager) getConnectionPlanInitial() ([]connectionAttemptParams, error) {
	var regionsToTry []*livekit.RegionInfo
	if !c.incomingRequestParams.disableRegionDiscovery {
		cloudHostname, _ := parseCloudURL(c.incomingRequestParams.url)
		if cloudHostname != "" {
			settings, err := c.regionProvider.RegionSettings(cloudHostname, c.token)
			if err == nil {
				regionsToTry = append(regionsToTry, settings.GetRegions()...)
			}
		}
	}

	// add the incoming request URL (i. e. original URL) just in case the region specific options did not work
	regionsToTry = append(regionsToTry, &livekit.RegionInfo{
		Region:   "original",
		Url:      c.incomingRequestParams.url,
		Distance: -1,
	})

	return c.buildConnectionPlan(c.incomingRequestParams.ctx, regionsToTry)
}

func (c *connectionManager) getConnectionPlanResuming() ([]connectionAttemptParams, error) {
	var regionsToTry []*livekit.RegionInfo
	if c.regionSettings != nil {
		// server sent list if available, the first entry should match the connected region
		if c.connectedRegion != nil {
			regions := c.regionSettings.GetRegions()
			if len(regions) > 0 && regions[0].Url != c.connectedRegion.Url {
				c.log.Infow(
					"first region in settings does not match connected region for resume",
					"firstRegion", protoLogger.Proto(regions[0]),
					"connectedRegion", protoLogger.Proto(c.connectedRegion),
				)
			}
		}

		// server sent list via LeaveRequest, try those
		regionsToTry = append(regionsToTry, c.regionSettings.GetRegions()...)
	} else {
		// no server sent list, try the connected url again
		if c.connectedRegion != nil {
			regionsToTry = append(regionsToTry, c.connectedRegion)
		}
	}

	// add the incoming request URL (i. e. original URL) just in case the region specific options did not work
	regionsToTry = append(regionsToTry, &livekit.RegionInfo{
		Region:   "original",
		Url:      c.incomingRequestParams.url,
		Distance: -1,
	})

	return c.buildConnectionPlan(context.Background(), regionsToTry)
}

func (c *connectionManager) getConnectionPlanReconnecting() ([]connectionAttemptParams, error) {
	var regionsToTry []*livekit.RegionInfo
	if c.regionSettings != nil {
		// server sent list via LeaveRequest, try those
		regionsToTry = append(regionsToTry, c.regionSettings.GetRegions()...)
	}

	// layer on region provider regions if enabled
	if !c.incomingRequestParams.disableRegionDiscovery {
		cloudHostname, _ := parseCloudURL(c.incomingRequestParams.url)
		if cloudHostname != "" {
			settings, err := c.regionProvider.RegionSettings(cloudHostname, c.token)
			if err == nil {
				regionsToTry = append(regionsToTry, settings.GetRegions()...)
			}
		}
	}

	// add the incoming request URL (i. e. original URL) just in case the region specific options did not work
	regionsToTry = append(regionsToTry, &livekit.RegionInfo{
		Region:   "original",
		Url:      c.incomingRequestParams.url,
		Distance: -1,
	})

	return c.buildConnectionPlan(context.Background(), regionsToTry)
}

func (c *connectionManager) buildConnectionPlan(ctx context.Context, regionsToTry []*livekit.RegionInfo) ([]connectionAttemptParams, error) {
	seen := make(map[string]bool, len(regionsToTry))
	dedupedRegions := make([]*livekit.RegionInfo, 0, len(regionsToTry))
	for _, region := range regionsToTry {
		if !seen[region.Region] {
			seen[region.Region] = true
			dedupedRegions = append(dedupedRegions, region)
		}
	}

	var plan []connectionAttemptParams
	for idx, region := range dedupedRegions {
		backoffWait := time.Duration(0)
		if idx != 0 {
			backoffWait = time.Duration(1<<min(idx-1, 6)) * 100 * time.Millisecond // max 6.4 seconds
		}

		plan = append(plan, connectionAttemptParams{
			ctx:             ctx,
			backoffWait:     backoffWait,
			region:          region,
			token:           c.token,
			validateTimeout: cValidateTimeout,
		})
	}
	return plan, nil
}

func (c *connectionManager) currentState() connectionManagerState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.state
}
