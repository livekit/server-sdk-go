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
	"net/http"

	"github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"
)

//lint:file-ignore SA1019 We still support some deprecated functions for backward compatibility

type SIPClient struct {
	sipClient livekit.SIP
	authBase
}

// NewSIPClient creates a LiveKit SIP client.
func NewSIPClient(url string, apiKey string, secretKey string, opts ...twirp.ClientOption) *SIPClient {
	return &SIPClient{
		sipClient: livekit.NewSIPProtobufClient(ToHttpURL(url), &http.Client{}, opts...),
		authBase: authBase{
			apiKey:    apiKey,
			apiSecret: secretKey,
		},
	}
}

// CreateSIPTrunk creates a new SIP Trunk.
//
// Deprecated: Use CreateSIPInboundTrunk or CreateSIPOutboundTrunk
func (s *SIPClient) CreateSIPTrunk(ctx context.Context, in *livekit.CreateSIPTrunkRequest) (*livekit.SIPTrunkInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.CreateSIPTrunk(ctx, in)
}

// CreateSIPInboundTrunk creates a new SIP Trunk for accepting inbound calls to LiveKit.
func (s *SIPClient) CreateSIPInboundTrunk(ctx context.Context, in *livekit.CreateSIPInboundTrunkRequest) (*livekit.SIPInboundTrunkInfo, error) {
	if in == nil || in.Trunk == nil || in.Trunk.SipTrunkId != "" {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.CreateSIPInboundTrunk(ctx, in)
}

// CreateSIPOutboundTrunk creates a new SIP Trunk for making outbound calls from LiveKit.
func (s *SIPClient) CreateSIPOutboundTrunk(ctx context.Context, in *livekit.CreateSIPOutboundTrunkRequest) (*livekit.SIPOutboundTrunkInfo, error) {
	if in == nil || in.Trunk == nil || in.Trunk.SipTrunkId != "" {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.CreateSIPOutboundTrunk(ctx, in)
}

// ListSIPTrunk lists SIP Trunks.
//
// Deprecated: Use ListSIPInboundTrunk or ListSIPOutboundTrunk
func (s *SIPClient) ListSIPTrunk(ctx context.Context, in *livekit.ListSIPTrunkRequest) (*livekit.ListSIPTrunkResponse, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.ListSIPTrunk(ctx, in)
}

// ListSIPInboundTrunk lists SIP Trunks accepting inbound calls.
func (s *SIPClient) ListSIPInboundTrunk(ctx context.Context, in *livekit.ListSIPInboundTrunkRequest) (*livekit.ListSIPInboundTrunkResponse, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.ListSIPInboundTrunk(ctx, in)
}

// ListSIPOutboundTrunk lists SIP Trunks for making outbound calls.
func (s *SIPClient) ListSIPOutboundTrunk(ctx context.Context, in *livekit.ListSIPOutboundTrunkRequest) (*livekit.ListSIPOutboundTrunkResponse, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.ListSIPOutboundTrunk(ctx, in)
}

// DeleteSIPTrunk deletes SIP Trunk given an ID.
func (s *SIPClient) DeleteSIPTrunk(ctx context.Context, in *livekit.DeleteSIPTrunkRequest) (*livekit.SIPTrunkInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.DeleteSIPTrunk(ctx, in)
}

// CreateSIPDispatchRule creates SIP Dispatch Rules.
func (s *SIPClient) CreateSIPDispatchRule(ctx context.Context, in *livekit.CreateSIPDispatchRuleRequest) (*livekit.SIPDispatchRuleInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.CreateSIPDispatchRule(ctx, in)
}

// ListSIPDispatchRule lists SIP Dispatch Rules.
func (s *SIPClient) ListSIPDispatchRule(ctx context.Context, in *livekit.ListSIPDispatchRuleRequest) (*livekit.ListSIPDispatchRuleResponse, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.ListSIPDispatchRule(ctx, in)
}

// DeleteSIPDispatchRule deletes SIP Dispatch Rule given an ID.
func (s *SIPClient) DeleteSIPDispatchRule(ctx context.Context, in *livekit.DeleteSIPDispatchRuleRequest) (*livekit.SIPDispatchRuleInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Admin: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.DeleteSIPDispatchRule(ctx, in)
}

// CreateSIPParticipant creates SIP Participant by making an outbound call.
func (s *SIPClient) CreateSIPParticipant(ctx context.Context, in *livekit.CreateSIPParticipantRequest) (*livekit.SIPParticipantInfo, error) {
	if in == nil {
		return nil, ErrInvalidParameter
	}

	ctx, err := s.withAuth(ctx, withSIPGrant{Call: true})
	if err != nil {
		return nil, err
	}
	return s.sipClient.CreateSIPParticipant(ctx, in)
}
