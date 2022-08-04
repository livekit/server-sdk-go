package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

type IngressClient struct {
	livekit.Ingress
	authBase
}

func NewIngressClient(url string, apiKey string, secretKey string) *IngressClient {
	url = ToHttpURL(url)
	client := livekit.NewIngressProtobufClient(url, &http.Client{})
	return &IngressClient{
		Ingress: client,
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

	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomJoin: true, Room: in.RoomName, CanPublish: getBoolPointer(true)})
	if err != nil {
		return nil, err
	}
	return c.Ingress.CreateIngress(ctx, in)
}

func (c *IngressClient) UpdateIngress(ctx context.Context, in *livekit.UpdateIngressRequest) (*livekit.IngressInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomJoin: true, Room: in.RoomName, CanPublish: getBoolPointer(true)})
	if err != nil {
		return nil, err
	}
	return c.Ingress.UpdateIngress(ctx, in)
}

func (c *IngressClient) ListIngress(ctx context.Context, in *livekit.ListIngressRequest) (*livekit.ListIngressResponse, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomJoin: true, Room: in.RoomName, CanPublish: getBoolPointer(true)})
	if err != nil {
		return nil, err
	}
	return c.Ingress.ListIngress(ctx, in)
}

func (c *IngressClient) DeleteIngress(ctx context.Context, in *livekit.DeleteIngressRequest) (*livekit.IngressInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomJoin: true, CanPublish: getBoolPointer(true)})
	if err != nil {
		return nil, err
	}
	return c.Ingress.DeleteIngress(ctx, in)
}

func getBoolPointer(v bool) *bool {
	return &v
}
