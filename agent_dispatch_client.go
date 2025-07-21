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

	"github.com/twitchtv/twirp"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/server-sdk-go/v2/signalling"
)

type AgentDispatchClient struct {
	agentDispatchService livekit.AgentDispatchService
	authBase
}

func NewAgentDispatchServiceClient(url string, apiKey string, secretKey string, opts ...twirp.ClientOption) *AgentDispatchClient {
	url = signalling.ToHttpURL(url)
	client := livekit.NewAgentDispatchServiceProtobufClient(url, &http.Client{}, opts...)

	return &AgentDispatchClient{
		agentDispatchService: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *AgentDispatchClient) CreateDispatch(ctx context.Context, req *livekit.CreateAgentDispatchRequest) (*livekit.AgentDispatch, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}

	return c.agentDispatchService.CreateDispatch(ctx, req)
}

func (c *AgentDispatchClient) DeleteDispatch(ctx context.Context, req *livekit.DeleteAgentDispatchRequest) (*livekit.AgentDispatch, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}

	return c.agentDispatchService.DeleteDispatch(ctx, req)
}

func (c *AgentDispatchClient) ListDispatch(ctx context.Context, req *livekit.ListAgentDispatchRequest) (*livekit.ListAgentDispatchResponse, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}

	return c.agentDispatchService.ListDispatch(ctx, req)
}
