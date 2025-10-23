package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/xtwirp"
	"github.com/livekit/server-sdk-go/v2/signalling"
	"github.com/twitchtv/twirp"
)

type ConnectorClient struct {
	connectorClient livekit.Connector
	authBase
}

func NewConnectorClient(url string, apiKey string, secretKey string, opts ...twirp.ClientOption) *ConnectorClient {
	opts = append(opts, xtwirp.DefaultClientOptions()...)
	url = signalling.ToHttpURL(url)
	client := livekit.NewConnectorProtobufClient(url, &http.Client{}, opts...)
	return &ConnectorClient{
		connectorClient: client,
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

func (c *ConnectorClient) DialWhatsAppCall(ctx context.Context, req *livekit.DialWhatsAppCallRequest) (*livekit.DialWhatsAppCallResponse, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true})
	if err != nil {
		return nil, err
	}
	return c.connectorClient.DialWhatsAppCall(ctx, req)
}

func (c *ConnectorClient) AcceptWhatsAppCall(ctx context.Context, req *livekit.AcceptWhatsAppCallRequest) (*livekit.AcceptWhatsAppCallResponse, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true})
	if err != nil {
		return nil, err
	}
	return c.connectorClient.AcceptWhatsAppCall(ctx, req)
}

func (c *ConnectorClient) ConnectWhatsAppCall(ctx context.Context, req *livekit.ConnectWhatsAppCallRequest) (*livekit.ConnectWhatsAppCallResponse, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true})
	if err != nil {
		return nil, err
	}
	return c.connectorClient.ConnectWhatsAppCall(ctx, req)
}

func (c *ConnectorClient) DisconnectWhatsAppCall(ctx context.Context, req *livekit.DisconnectWhatsAppCallRequest) (*livekit.DisconnectWhatsAppCallResponse, error) {
	ctx, err := c.withAuth(ctx, withVideoGrant{RoomAdmin: true})
	if err != nil {
		return nil, err
	}
	return c.connectorClient.DisconnectWhatsAppCall(ctx, req)
}
