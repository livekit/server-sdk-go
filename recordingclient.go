package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/auth"

	livekit "github.com/livekit/server-sdk-go/proto"
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

func (c *RecordingServiceClient) StartRecording(ctx context.Context, req *livekit.StartRecordingRequest) (*livekit.RecordingResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.RecordingService.StartRecording(ctx, req)
}

func (c *RecordingServiceClient) EndRecording(ctx context.Context, req *livekit.EndRecordingRequest) (*livekit.RecordingResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.RecordingService.EndRecording(ctx, req)
}
