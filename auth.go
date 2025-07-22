// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lksdk

import (
	"context"

	"github.com/twitchtv/twirp"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/server-sdk-go/v2/signalling"
)

type authBase struct {
	apiKey    string
	apiSecret string
}

type authOption interface {
	Apply(t *auth.AccessToken)
}

type withVideoGrant auth.VideoGrant

func (g withVideoGrant) Apply(t *auth.AccessToken) {
	t.SetVideoGrant((*auth.VideoGrant)(&g))
}

type withSIPGrant auth.SIPGrant

func (g withSIPGrant) Apply(t *auth.AccessToken) {
	t.SetSIPGrant((*auth.SIPGrant)(&g))
}

type withAgentGrant auth.AgentGrant

func (g withAgentGrant) Apply(t *auth.AccessToken) {
	t.SetAgentGrant((*auth.AgentGrant)(&g))
}

func (b authBase) withAuth(ctx context.Context, opt authOption, options ...authOption) (context.Context, error) {
	at := auth.NewAccessToken(b.apiKey, b.apiSecret)
	opt.Apply(at)
	for _, opt := range options {
		opt.Apply(at)
	}
	token, err := at.ToJWT()
	if err != nil {
		return nil, err
	}

	return twirp.WithHTTPRequestHeaders(ctx, signalling.NewHTTPHeaderWithToken(token))
}
