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
	"sync"

	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
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

	asyncTransportLock sync.RWMutex
	asyncTransport     SignalTransport
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
	if asyncTransport := s.getAsyncTransport(); asyncTransport != nil {
		asyncTransport.SetLogger(l)
	}
}

func (s *signalTransportHybrid) SetAsyncTransport(asyncTransport SignalTransport) {
	s.asyncTransportLock.Lock()
	defer s.asyncTransportLock.Unlock()

	s.asyncTransport = asyncTransport
}

func (s *signalTransportHybrid) getAsyncTransport() SignalTransport {
	s.asyncTransportLock.RLock()
	defer s.asyncTransportLock.RUnlock()

	return s.asyncTransport
}

func (s *signalTransportHybrid) Start() {
	s.syncTransport.Start()
	if asyncTransport := s.getAsyncTransport(); asyncTransport != nil {
		asyncTransport.Start()
	}
}

func (s *signalTransportHybrid) IsStarted() bool {
	if asyncTransport := s.getAsyncTransport(); asyncTransport != nil {
		return asyncTransport.IsStarted()
	}
	return s.syncTransport.IsStarted()
}

func (s *signalTransportHybrid) Close() {
	s.syncTransport.Close()
	if asyncTransport := s.getAsyncTransport(); asyncTransport != nil {
		asyncTransport.Close()
	}
}

func (s *signalTransportHybrid) Join(
	ctx context.Context,
	url string,
	token string,
	connectParams ConnectParams,
	publisherOffer webrtc.SessionDescription,
) error {
	return s.syncTransport.Join(ctx, url, token, connectParams, publisherOffer)
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
	if asyncTransport := s.getAsyncTransport(); asyncTransport != nil {
		return asyncTransport.SendMessage(msg)
	}

	return s.syncTransport.SendMessage(msg)
}
