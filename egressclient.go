package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

type EgressClient struct {
	egressClient livekit.Egress
	authBase
}

func NewEgressClient(url string, apiKey string, secretKey string) *EgressClient {
	url = ToHttpURL(url)
	client := livekit.NewEgressProtobufClient(url, &http.Client{})
	return &EgressClient{
		egressClient: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *EgressClient) StartRoomCompositeEgress(ctx context.Context, req *livekit.RoomCompositeEgressRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.egressClient.StartRoomCompositeEgress(ctx, req)
}

func (c *EgressClient) StartTrackCompositeEgress(ctx context.Context, req *livekit.TrackCompositeEgressRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.egressClient.StartTrackCompositeEgress(ctx, req)
}

func (c *EgressClient) StartTrackEgress(ctx context.Context, req *livekit.TrackEgressRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.egressClient.StartTrackEgress(ctx, req)
}

func (c *EgressClient) StartWebEgress(ctx context.Context, req *livekit.WebEgressRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.egressClient.StartWebEgress(ctx, req)
}

func (c *EgressClient) UpdateLayout(ctx context.Context, req *livekit.UpdateLayoutRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.egressClient.UpdateLayout(ctx, req)
}

func (c *EgressClient) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.egressClient.UpdateStream(ctx, req)
}

func (c *EgressClient) ListEgress(ctx context.Context, req *livekit.ListEgressRequest) (*livekit.ListEgressResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.egressClient.ListEgress(ctx, req)
}

func (c *EgressClient) StopEgress(ctx context.Context, req *livekit.StopEgressRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.egressClient.StopEgress(ctx, req)
}
