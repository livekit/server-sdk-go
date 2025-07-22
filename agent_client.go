package lksdk

import (
	"context"
	"net/http"
	"os"
	"regexp"

	"github.com/twitchtv/twirp"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/server-sdk-go/v2/signalling"
)

type AgentClient struct {
	agentClient livekit.CloudAgent
	authBase
}

func NewAgentClient(url string, apiKey string, apiSecret string, opts ...twirp.ClientOption) (*AgentClient, error) {
	serverUrl := os.Getenv("LK_AGENTS_URL")
	if serverUrl == "" {
		url = signalling.ToHttpURL(url)
		pattern := `^https?://[^.]+\.`
		re := regexp.MustCompile(pattern)
		serverUrl = re.ReplaceAllString(url, "https://agents.")
	}

	client := livekit.NewCloudAgentProtobufClient(serverUrl, &http.Client{}, opts...)
	return &AgentClient{
		agentClient: client,
		authBase:    authBase{apiKey, apiSecret},
	}, nil
}

func (c *AgentClient) CreateAgent(ctx context.Context, req *livekit.CreateAgentRequest) (*livekit.CreateAgentResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.CreateAgent(ctx, req)
}

func (c *AgentClient) ListAgents(ctx context.Context, req *livekit.ListAgentsRequest) (*livekit.ListAgentsResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.ListAgents(ctx, req)
}

func (c *AgentClient) ListAgentVersions(ctx context.Context, req *livekit.ListAgentVersionsRequest) (*livekit.ListAgentVersionsResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.ListAgentVersions(ctx, req)
}

func (c *AgentClient) DeleteAgent(ctx context.Context, req *livekit.DeleteAgentRequest) (*livekit.DeleteAgentResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.DeleteAgent(ctx, req)
}

func (c *AgentClient) UpdateAgent(ctx context.Context, req *livekit.UpdateAgentRequest) (*livekit.UpdateAgentResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.UpdateAgent(ctx, req)
}

func (c *AgentClient) RestartAgent(ctx context.Context, req *livekit.RestartAgentRequest) (*livekit.RestartAgentResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.RestartAgent(ctx, req)
}

func (c *AgentClient) RollbackAgent(ctx context.Context, req *livekit.RollbackAgentRequest) (*livekit.RollbackAgentResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.RollbackAgent(ctx, req)
}

func (c *AgentClient) ListAgentSecrets(ctx context.Context, req *livekit.ListAgentSecretsRequest) (*livekit.ListAgentSecretsResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.ListAgentSecrets(ctx, req)
}

func (c *AgentClient) UpdateAgentSecrets(ctx context.Context, req *livekit.UpdateAgentSecretsRequest) (*livekit.UpdateAgentSecretsResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.UpdateAgentSecrets(ctx, req)
}

func (c *AgentClient) DeployAgent(ctx context.Context, req *livekit.DeployAgentRequest) (*livekit.DeployAgentResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.DeployAgent(ctx, req)
}

func (c *AgentClient) GetClientSettings(ctx context.Context, req *livekit.ClientSettingsRequest) (*livekit.ClientSettingsResponse, error) {
	ctx, err := c.withAuth(ctx, withAgentGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return c.agentClient.GetClientSettings(ctx, req)
}
