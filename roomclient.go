package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

type RoomServiceClient struct {
	livekit.RoomService
	authBase
}

func NewRoomServiceClient(url string, apiKey string, secretKey string) *RoomServiceClient {
	url = ToHttpURL(url)
	client := livekit.NewRoomServiceProtobufClient(url, &http.Client{})
	return &RoomServiceClient{
		RoomService: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *RoomServiceClient) CreateRoom(ctx context.Context, req *livekit.CreateRoomRequest) (*livekit.Room, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomCreate: true})
	if err != nil {
		return nil, err
	}

	return c.RoomService.CreateRoom(ctx, req)
}

func (c *RoomServiceClient) ListRooms(ctx context.Context, req *livekit.ListRoomsRequest) (*livekit.ListRoomsResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomList: true})
	if err != nil {
		return nil, err
	}

	return c.RoomService.ListRooms(ctx, req)
}

func (c *RoomServiceClient) DeleteRoom(ctx context.Context, req *livekit.DeleteRoomRequest) (*livekit.DeleteRoomResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomCreate: true})
	if err != nil {
		return nil, err
	}

	return c.RoomService.DeleteRoom(ctx, req)
}

func (c *RoomServiceClient) ListParticipants(ctx context.Context, req *livekit.ListParticipantsRequest) (*livekit.ListParticipantsResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}

	return c.RoomService.ListParticipants(ctx, req)
}

func (c *RoomServiceClient) GetParticipant(ctx context.Context, req *livekit.RoomParticipantIdentity) (*livekit.ParticipantInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}

	return c.RoomService.GetParticipant(ctx, req)
}

func (c *RoomServiceClient) RemoveParticipant(ctx context.Context, req *livekit.RoomParticipantIdentity) (*livekit.RemoveParticipantResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}

	return c.RoomService.RemoveParticipant(ctx, req)
}

func (c *RoomServiceClient) MutePublishedTrack(ctx context.Context, req *livekit.MuteRoomTrackRequest) (*livekit.MuteRoomTrackResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}

	return c.RoomService.MutePublishedTrack(ctx, req)
}

func (c *RoomServiceClient) UpdateParticipant(ctx context.Context, req *livekit.UpdateParticipantRequest) (*livekit.ParticipantInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}
	return c.RoomService.UpdateParticipant(ctx, req)
}

func (c *RoomServiceClient) UpdateSubscriptions(ctx context.Context, req *livekit.UpdateSubscriptionsRequest) (*livekit.UpdateSubscriptionsResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}
	return c.RoomService.UpdateSubscriptions(ctx, req)
}

func (c *RoomServiceClient) UpdateRoomMetadata(ctx context.Context, req *livekit.UpdateRoomMetadataRequest) (*livekit.Room, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}
	return c.RoomService.UpdateRoomMetadata(ctx, req)
}

func (c *RoomServiceClient) SendData(ctx context.Context, req *livekit.SendDataRequest) (*livekit.SendDataResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomAdmin: true, Room: req.Room})
	if err != nil {
		return nil, err
	}
	return c.RoomService.SendData(ctx, req)
}

func (c *RoomServiceClient) CreateToken() *auth.AccessToken {
	return auth.NewAccessToken(c.apiKey, c.apiSecret)
}
