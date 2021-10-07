package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/auth"
	livekit "github.com/livekit/protocol/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type RecordingServiceClient struct {
	livekit.RecordingService
	authBase
}

func NewRecordingServiceClient(url string, apiKey string, secretKey string) *RecordingServiceClient {
	client := livekit.NewRecordingServiceProtobufClient(url, &http.Client{})
	return &RecordingServiceClient{
		RecordingService: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *RecordingServiceClient) StartRecording(ctx context.Context, req *livekit.StartRecordingRequest) (*livekit.StartRecordingResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.RecordingService.StartRecording(ctx, req)
}

func (c *RecordingServiceClient) AddOutput(ctx context.Context, req *livekit.AddOutputRequest) (*emptypb.Empty, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.RecordingService.AddOutput(ctx, req)
}

func (c *RecordingServiceClient) RemoveOutput(ctx context.Context, req *livekit.RemoveOutputRequest) (*emptypb.Empty, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.RecordingService.RemoveOutput(ctx, req)
}

func (c *RecordingServiceClient) EndRecording(ctx context.Context, req *livekit.EndRecordingRequest) (*emptypb.Empty, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.RecordingService.EndRecording(ctx, req)
}
