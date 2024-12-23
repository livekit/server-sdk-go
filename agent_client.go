// Copyright 2023 LiveKit, Inc.
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
	"net/http"

	"github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"
)

type AgentClient struct {
	agentService livekit.AgentService
	authBase
}

func NewAgentServiceClient(url string, apiKey string, secretKey string, opts ...twirp.ClientOption) *AgentClient {
	url = ToHttpURL(url)
	client := livekit.NewAgentServiceProtobufClient(url, &http.Client{}, opts...)

	return &AgentClient{
		agentService: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *AgentClient) CheckEnabled(ctx context.Context, req *livekit.CheckEnabledRequest) (*livekit.CheckEnabledResponse, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true})
	if err != nil {
		return nil, err
	}

	return c.agentService.CheckEnabled(ctx, req)
}

func (c *AgentClient) CheckAvailibility(ctx context.Context, req *livekit.CheckAvailibilityRequest) (*livekit.CheckAvailibilityResponse, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true})
	if err != nil {
		return nil, err
	}

	return c.agentService.CheckAvailibility(ctx, req)
}
