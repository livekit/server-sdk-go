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

package signalling

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"runtime"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"google.golang.org/protobuf/proto"
)

var _ Signalling = (*signallingv2)(nil)

type Signallingv2Params struct {
	Logger logger.Logger
}

type signallingv2 struct {
	signallingUnimplemented

	params Signallingv2Params
}

func NewSignallingv2(params Signallingv2Params) Signalling {
	return &signallingv2{
		params: params,
	}
}

func (s *signallingv2) SetLogger(l logger.Logger) {
	s.params.Logger = l
}

func (s *signallingv2) Path() string {
	return "/rtc/v2"
}

func (s *signallingv2) ParticipantPath(participantSid string) string {
	return "/rtc/v2/" + participantSid
}

func (s *signallingv2) ValidatePath() string {
	return "/rtc/v2/validate"
}

func (s *signallingv2) JoinMethod() joinMethod {
	return joinMethodConnectRequest
}

func (s *signallingv2) ConnectRequest(
	version string,
	protocol int,
	connectParams *ConnectParams,
	participantSID string,
) (*livekit.ConnectRequest, error) {
	clientInfo := &livekit.ClientInfo{
		Version:  version,
		Protocol: int32(protocol),
		Os:       runtime.GOOS,
		Sdk:      livekit.ClientInfo_GO,
	}

	connectionSettings := &livekit.ConnectionSettings{}
	if connectParams.AutoSubscribe {
		connectionSettings.AutoSubscribe = true
	}

	return &livekit.ConnectRequest{
		ClientInfo:            clientInfo,
		ConnectionSettings:    connectionSettings,
		ParticipantAttributes: connectParams.Attributes,
	}, nil
}

func (s *signallingv2) HTTPRequestForValidate(
	ctx context.Context,
	version string,
	protocol int,
	urlPrefix string,
	token string,
	connectParams *ConnectParams,
	participantSID string,
) (*http.Request, error) {
	if urlPrefix == "" {
		return nil, ErrURLNotProvided
	}

	connectRequest, err := s.ConnectRequest(
		version,
		protocol,
		connectParams,
		participantSID,
	)
	if err != nil {
		return nil, err
	}

	wireMessage := s.SignalConnectRequest(connectRequest)
	protoMsg, err := proto.Marshal(wireMessage)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(ToHttpURL(urlPrefix) + s.ValidatePath())
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), bytes.NewBuffer(protoMsg))
	if err != nil {
		s.params.Logger.Errorw("error creating validate request", err)
		return nil, err
	}
	req.Header = NewHTTPHeaderWithToken(token)
	req.Header.Set("Content-Type", "application/x-protobuf")
	return req, nil
}

func (s *signallingv2) AckMessageId(ackMessageId uint32) {
	// SIGNALLING-V2-TODO s.signalCache.Clear(ackMessageId)
}

func (s *signallingv2) SetLastProcessedRemoteMessageId(lastProcessedRemoteMessageId uint32) {
	// SIGNALLING-V2-TODO s.signalCache.SetLastProcessedRemoteMessageId(lastProcessedRemoteMessageId)
}

func (s *signallingv2) SignalConnectRequest(connectRequest *livekit.ConnectRequest) proto.Message {
	clientMessage := &livekit.Signalv2ClientMessage{
		Message: &livekit.Signalv2ClientMessage_ConnectRequest{
			ConnectRequest: connectRequest,
		},
	}
	return s.cacheAndReturnEnvelope(clientMessage)
}

func (s *signallingv2) SignalSdpOffer(offer *livekit.SessionDescription) proto.Message {
	clientMessage := &livekit.Signalv2ClientMessage{
		Message: &livekit.Signalv2ClientMessage_PublisherSdp{
			PublisherSdp: offer,
		},
	}
	return s.cacheAndReturnEnvelope(clientMessage)
}

func (s *signallingv2) SignalSdpAnswer(answer *livekit.SessionDescription) proto.Message {
	clientMessage := &livekit.Signalv2ClientMessage{
		Message: &livekit.Signalv2ClientMessage_SubscriberSdp{
			SubscriberSdp: answer,
		},
	}
	return s.cacheAndReturnEnvelope(clientMessage)
}

func (s *signallingv2) cacheAndReturnEnvelope(cm *livekit.Signalv2ClientMessage) proto.Message {
	/* SIGNALLING-V2-TODO
	sm = s.signalCache.Add(sm)
	if sm == nil {
		return nil
	}
	*/

	return &livekit.Signalv2WireMessage{
		Message: &livekit.Signalv2WireMessage_Envelope{
			Envelope: &livekit.Envelope{
				ClientMessages: []*livekit.Signalv2ClientMessage{cm},
			},
		},
	}
}
