package lksdk

import (
	"context"
	"net/http"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

type ObfuscatedClient struct {
	obfuscatedClient livekit.Obfuscated
	authBase
}

func NewObfuscatedClient(url string, apiKey string, secretKey string) *ObfuscatedClient {
	url = ToHttpURL(url)
	client := livekit.NewObfuscatedProtobufClient(url, &http.Client{})
	return &ObfuscatedClient{
		obfuscatedClient: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *ObfuscatedClient) A(ctx context.Context, req *livekit.ARequest) (*livekit.AResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{})
	if err != nil {
		return nil, err
	}
	return c.obfuscatedClient.A(ctx, req)
}

func (c *ObfuscatedClient) B(ctx context.Context, req *livekit.BRequest) (*emptypb.Empty, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{})
	if err != nil {
		return nil, err
	}
	return c.obfuscatedClient.B(ctx, req)
}

func (c *ObfuscatedClient) C(ctx context.Context, req *livekit.CRequest) (*emptypb.Empty, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{})
	if err != nil {
		return nil, err
	}
	return c.obfuscatedClient.C(ctx, req)
}
