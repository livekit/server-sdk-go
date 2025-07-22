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
	"fmt"
	"sync/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	protosignalling "github.com/livekit/protocol/signalling"
	"google.golang.org/protobuf/proto"
)

var _ SignalHandler = (*signalhandlerv2)(nil)

type SignalHandlerv2Params struct {
	Logger     logger.Logger
	Processor  SignalProcessor
	Signalling Signalling
}

type signalhandlerv2 struct {
	signalhandlerUnimplemented

	params SignalHandlerv2Params

	lastProcessedRemoteMessageId atomic.Uint32
	signalReassembler            *protosignalling.SignalReassembler
}

func NewSignalHandlerv2(params SignalHandlerv2Params) SignalHandler {
	return &signalhandlerv2{
		params: params,
		signalReassembler: protosignalling.NewSignalReassembler(protosignalling.SignalReassemblerParams{
			Logger: params.Logger,
		}),
	}
}

func (s *signalhandlerv2) SetLogger(l logger.Logger) {
	s.params.Logger = l
}

func (s *signalhandlerv2) HandleMessage(msg proto.Message) error {
	wireMessage, ok := msg.(*livekit.Signalv2WireMessage)
	if !ok {
		s.params.Logger.Warnw(
			"unknown message type", nil,
			"messageType", fmt.Sprintf("%T", msg),
		)
		return ErrInvalidMessageType
	}

	switch msg := wireMessage.GetMessage().(type) {
	case *livekit.Signalv2WireMessage_Envelope:
		for _, serverMessage := range msg.Envelope.ServerMessages {
			// SIGNAL-V2-TODO: cannot do this comparison for very first message
			if serverMessage.Sequencer.MessageId != s.lastProcessedRemoteMessageId.Load()+1 {
				s.params.Logger.Infow(
					"gap in message stream",
					"last", s.lastProcessedRemoteMessageId.Load(),
					"current", serverMessage.Sequencer.MessageId,
				)
			}

			// SIGNALLING-V2-TODO: ask for replay if there are gaps

			// SIGNALLING-V2-TODO: process messages
			switch payload := serverMessage.GetMessage().(type) {
			case *livekit.Signalv2ServerMessage_PublisherSdp:
				s.params.Processor.OnAnswer(protosignalling.FromProtoSessionDescription(payload.PublisherSdp))

			case *livekit.Signalv2ServerMessage_SubscriberSdp:
				s.params.Processor.OnOffer(protosignalling.FromProtoSessionDescription(payload.SubscriberSdp))
			}

			s.lastProcessedRemoteMessageId.Store(serverMessage.Sequencer.MessageId)
			s.params.Signalling.AckMessageId(serverMessage.Sequencer.LastProcessedRemoteMessageId)
			s.params.Signalling.SetLastProcessedRemoteMessageId(serverMessage.Sequencer.MessageId)
		}

	case *livekit.Signalv2WireMessage_Fragment:
		bytes := s.signalReassembler.Reassemble(msg.Fragment)
		if len(bytes) != 0 {
			wireMessage := &livekit.Signalv2WireMessage{}
			err := proto.Unmarshal(bytes, wireMessage)
			if err != nil {
				s.params.Logger.Warnw("could not unmarshal re-assembled packet", err)
				return err
			}

			s.HandleMessage(wireMessage)
		}
	}

	return nil
}
