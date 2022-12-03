package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

type IngressClient struct {
	ingressClient livekit.Ingress
	authBase
}

func NewIngressClient(url string, apiKey string, secretKey string) *IngressClient {
	url = ToHttpURL(url)
	client := livekit.NewIngressProtobufClient(url, &http.Client{})
	return &IngressClient{
		ingressClient: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *IngressClient) CreateIngress(ctx context.Context, in *livekit.CreateIngressRequest) (*livekit.IngressInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := c.withAuth(ctx, auth.VideoGrant{IngressAdmin: true})
	if err != nil {
		return nil, err
	}
	return c.ingressClient.CreateIngress(ctx, in)
}

func (c *IngressClient) UpdateIngress(ctx context.Context, in *livekit.UpdateIngressRequest) (*livekit.IngressInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := c.withAuth(ctx, auth.VideoGrant{IngressAdmin: true})
	if err != nil {
		return nil, err
	}
	return c.ingressClient.UpdateIngress(ctx, in)
}

func (c *IngressClient) ListIngress(ctx context.Context, in *livekit.ListIngressRequest) (*livekit.ListIngressResponse, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := c.withAuth(ctx, auth.VideoGrant{IngressAdmin: true})
	if err != nil {
		return nil, err
	}
	return c.ingressClient.ListIngress(ctx, in)
}

func (c *IngressClient) DeleteIngress(ctx context.Context, in *livekit.DeleteIngressRequest) (*livekit.IngressInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := c.withAuth(ctx, auth.VideoGrant{IngressAdmin: true})
	if err != nil {
		return nil, err
	}
	return c.ingressClient.DeleteIngress(ctx, in)
}
