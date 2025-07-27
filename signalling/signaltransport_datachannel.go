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
	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"
)

var _ SignalTransport = (*signalTransportDataChannel)(nil)

type SignalTransportDataChannelParams struct {
	Logger        logger.Logger
	DataChannel   *webrtc.DataChannel
	SignalHandler SignalHandler
}

type signalTransportDataChannel struct {
	signalTransportUnimplemented

	params SignalTransportDataChannelParams

	mq *messageQueue
}

func NewSignalTransportDataChannel(params SignalTransportDataChannelParams) SignalTransport {
	s := &signalTransportDataChannel{
		params: params,
	}
	s.params.DataChannel.OnMessage(func(dataMsg webrtc.DataChannelMessage) {
		s.params.SignalHandler.HandleEncodedMessage(dataMsg.Data)
	})
	s.mq = newMessageQueue(messageQueueParams{
		Logger:        params.Logger,
		HandleMessage: s.handleMessage,
	})
	return s
}

func (s *signalTransportDataChannel) SetLogger(l logger.Logger) {
	s.params.Logger = l
	s.mq.SetLogger(l)
}

func (s *signalTransportDataChannel) Start() {
	s.mq.Start()
}

func (s *signalTransportDataChannel) IsStarted() bool {
	return s.mq.IsStarted()
}

func (s *signalTransportDataChannel) Close() {
	s.mq.Close()
}

func (s *signalTransportDataChannel) SendMessage(msg proto.Message) error {
	return s.mq.Enqueue(msg)
}

func (s *signalTransportDataChannel) handleMessage(msg proto.Message) {
	protoMsg, err := proto.Marshal(msg)
	if err != nil {
		s.params.Logger.Errorw("could not marshal proto message", err)
		return
	}

	if err := s.params.DataChannel.Send(protoMsg); err != nil {
		s.params.Logger.Errorw("could not send proto message", err)
		return
	}
}
