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
	"context"

	"github.com/livekit/protocol/logger"
	"google.golang.org/protobuf/proto"
)

var _ SignalTransport = (*signalTransportHybrid)(nil)

type SignalTransportHybridParams struct {
	Logger        logger.Logger
	Version       string
	Protocol      int
	Signalling    Signalling
	SignalHandler SignalHandler
}

type signalTransportHybrid struct {
	signalTransportUnimplemented

	params SignalTransportHybridParams

	syncTransport SignalTransport
}

func NewSignalTransportHybrid(params SignalTransportHybridParams) SignalTransport {
	return &signalTransportHybrid{
		params: params,
		syncTransport: NewSignalTransportHttp(SignalTransportHttpParams{
			Logger:        params.Logger,
			Version:       params.Version,
			Protocol:      params.Protocol,
			Signalling:    params.Signalling,
			SignalHandler: params.SignalHandler,
		}),
	}
}

func (s *signalTransportHybrid) SetLogger(l logger.Logger) {
	s.params.Logger = l
	s.syncTransport.SetLogger(l)
}

func (s *signalTransportHybrid) Join(
	ctx context.Context,
	url string,
	token string,
	connectParams ConnectParams,
) error {
	return s.syncTransport.Join(ctx, url, token, connectParams)
}

func (s *signalTransportHybrid) Reconnect(
	url string,
	token string,
	connectParams ConnectParams,
	participantSID string,
) error {
	return s.syncTransport.Reconnect(url, token, connectParams, participantSID)
}

func (s *signalTransportHybrid) SetParticipantResource(url string, participantSid string, token string) {
	s.syncTransport.SetParticipantResource(url, participantSid, token)
}

func (s *signalTransportHybrid) UpdateParticipantToken(token string) {
	s.syncTransport.UpdateParticipantToken(token)
}

func (s *signalTransportHybrid) SendMessage(msg proto.Message) error {
	return s.syncTransport.SendMessage(msg)
}
