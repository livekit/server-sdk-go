package lksdk

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

type EgressClient struct {
	livekit.Egress
	authBase
}

func NewEgressClient(url string, apiKey string, secretKey string) *EgressClient {
	client := livekit.NewEgressProtobufClient(url, &http.Client{})
	return &EgressClient{
		Egress: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *EgressClient) StartWebCompositeEgress(ctx context.Context, req *livekit.WebCompositeEgressRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.Egress.StartWebCompositeEgress(ctx, req)
}

// func (c *EgressClient) StartTrackCompositeEgress(ctx context.Context, req *livekit.TrackCompositeEgressRequest) (*livekit.EgressInfo, error) {
// 	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
// 	if err != nil {
// 		return nil, err
// 	}
// 	return c.Egress.StartTrackCompositeEgress(ctx, req)
// }
//
// func (c *EgressClient) StartTrackEgress(ctx context.Context, req *livekit.TrackEgressRequest) (*livekit.EgressInfo, error) {
// 	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
// 	if err != nil {
// 		return nil, err
// 	}
// 	return c.Egress.StartTrackEgress(ctx, req)
// }

func (c *EgressClient) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.Egress.UpdateStream(ctx, req)
}

func (c *EgressClient) ListEgress(ctx context.Context, req *livekit.ListEgressRequest) (*livekit.ListEgressResponse, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.Egress.ListEgress(ctx, req)
}

func (c *EgressClient) StopEgress(ctx context.Context, req *livekit.StopEgressRequest) (*livekit.EgressInfo, error) {
	ctx, err := c.withAuth(ctx, auth.VideoGrant{RoomRecord: true})
	if err != nil {
		return nil, err
	}
	return c.Egress.StopEgress(ctx, req)
}

// GeneratePresignedURL creates an upload URL for file results
func GeneratePresignedURL(accessKey, secret, endpoint, region, bucket, key string) (string, error) {
	sess, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(accessKey, secret, ""),
		Endpoint:    aws.String(endpoint),
		Region:      aws.String(region),
	})
	if err != nil {
		return "", err
	}
	req, _ := s3.New(sess).PutObjectRequest(&s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	url, err := req.Presign(time.Hour * 24)
	if err != nil {
		return "", err
	}
	return url, nil
}
