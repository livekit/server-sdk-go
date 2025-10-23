// Copyright 2025 LiveKit, Inc.
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

package cloudagents

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"

	lkproto "github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/twitchtv/twirp"
)

// Client is a wrapper around the lksdk.AgentClient that provides a simpler interface for creating and deploying agents.
type Client struct {
	*lksdk.AgentClient
	projectURL string
	apiKey     string
	apiSecret  string
	agentsURL  string
	httpClient *http.Client
	headers    map[string]string
	logger     logger.Logger
}

// New returns a new Client with the given project URL, API key, and API secret.
func New(opts ...ClientOption) (*Client, error) {
	client := &Client{
		logger: logger.GetLogger(),
	}
	for _, opt := range opts {
		opt(client)
	}
	if client.projectURL == "" {
		return nil, fmt.Errorf("project credentials are required")
	}
	agentClient, err := lksdk.NewAgentClient(client.projectURL, client.apiKey, client.apiSecret, twirp.WithClientHooks(&twirp.ClientHooks{
		RequestPrepared: func(ctx context.Context, req *http.Request) (context.Context, error) {
			client.setLivekitHeaders(req)
			return ctx, nil
		},
	}))
	if err != nil {
		return nil, err
	}
	client.AgentClient = agentClient
	client.agentsURL = client.getAgentsURL("")
	if client.httpClient == nil {
		client.httpClient = &http.Client{}
	}
	return client, nil
}

// CreateAgent creates a new agent by building from source.
func (c *Client) CreateAgent(
	ctx context.Context,
	source fs.FS,
	secrets []*lkproto.AgentSecret,
	regions []string,
	excludeFiles []string,
	buildLogStreamWriter io.Writer,
) (*lkproto.CreateAgentResponse, error) {
	resp, err := c.AgentClient.CreateAgent(ctx, &lkproto.CreateAgentRequest{
		Secrets: secrets,
		Regions: regions,
	})
	if err != nil {
		return nil, err
	}
	if err := c.uploadAndBuild(ctx,
		resp.AgentId,
		resp.PresignedUrl,
		resp.PresignedPostRequest,
		source,
		excludeFiles,
		buildLogStreamWriter,
	); err != nil {
		return nil, err
	}
	return resp, nil
}

// DeployAgent deploys new agent by building from source.
func (c *Client) DeployAgent(
	ctx context.Context,
	agentID string,
	source fs.FS,
	secrets []*lkproto.AgentSecret,
	excludeFiles []string,
	buildLogStreamWriter io.Writer,
) error {
	resp, err := c.AgentClient.DeployAgent(ctx, &lkproto.DeployAgentRequest{
		AgentId: agentID,
		Secrets: secrets,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("failed to deploy agent: %s", resp.Message)
	}
	return c.uploadAndBuild(ctx, agentID, resp.PresignedUrl, resp.PresignedPostRequest, source, excludeFiles, buildLogStreamWriter)
}

// uploadAndBuild uploads the source and triggers remote build
func (c *Client) uploadAndBuild(
	ctx context.Context,
	agentID string,
	presignedUrl string,
	presignedPostRequest *lkproto.PresignedPostRequest,
	source fs.FS,
	excludeFiles []string,
	buildLogStreamWriter io.Writer,
) error {
	if err := uploadSource(
		source,
		presignedUrl,
		presignedPostRequest,
		excludeFiles,
	); err != nil {
		return err
	}
	if err := c.build(ctx, agentID, buildLogStreamWriter); err != nil {
		return err
	}
	return nil
}

func (c *Client) getAgentsURL(serverRegion string) string {
	agentsURL := c.projectURL
	if os.Getenv("LK_AGENTS_URL") != "" {
		agentsURL = os.Getenv("LK_AGENTS_URL")
	}
	if strings.HasPrefix(agentsURL, "ws") {
		agentsURL = strings.Replace(agentsURL, "ws", "http", 1)
	}
	if !strings.Contains(agentsURL, "localhost") && !strings.Contains(agentsURL, "127.0.0.1") {
		pattern := `^https://[a-zA-Z0-9\-]+\.`
		re := regexp.MustCompile(pattern)
		if serverRegion != "" {
			serverRegion = fmt.Sprintf("%s.", serverRegion)
		}
		agentsURL = re.ReplaceAllString(agentsURL, fmt.Sprintf("https://%sagents.", serverRegion))
	}
	return agentsURL
}
