package lksdk

import (
	"context"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/twitchtv/twirp"
)

type authBase struct {
	apiKey    string
	apiSecret string
}

func (b authBase) withAuth(ctx context.Context, grant auth.VideoGrant) (context.Context, error) {
	at := auth.NewAccessToken(b.apiKey, b.apiSecret)
	at.AddGrant(&grant)
	token, err := at.ToJWT()
	if err != nil {
		return nil, err
	}

	return twirp.WithHTTPRequestHeaders(ctx, newHeaderWithToken(token))
}

func newHeaderWithToken(token string) http.Header {
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token)
	return header
}
