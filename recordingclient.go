package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/twitchtv/twirp"

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

func (c *RecordingServiceClient) withAuth(ctx context.Context, grant auth.VideoGrant) (context.Context, error) {
	at := auth.NewAccessToken(c.apiKey, c.apiSecret)
	at.AddGrant(&grant)
	token, err := at.ToJWT()
	if err != nil {
		return nil, err
	}

	return twirp.WithHTTPRequestHeaders(ctx, newHeaderWithToken(token))
}
