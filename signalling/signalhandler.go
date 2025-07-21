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

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	protosignalling "github.com/livekit/protocol/signalling"
	"google.golang.org/protobuf/proto"
)

var _ SignalHandler = (*signalhandler)(nil)

type SignalHandlerParams struct {
	Logger    logger.Logger
	Processor SignalProcessor
}

type signalhandler struct {
	signalhandlerUnimplemented

	params SignalHandlerParams
}

func NewSignalHandler(params SignalHandlerParams) SignalHandler {
	return &signalhandler{
		params: params,
	}
}

func (s *signalhandler) HandleMessage(msg proto.Message) error {
	rsp, ok := msg.(*livekit.SignalResponse)
	if !ok {
		s.params.Logger.Warnw(
			"unknown message type", nil,
			"messageType", fmt.Sprintf("%T", msg),
		)
		return ErrInvalidMessageType
	}

	switch payload := rsp.GetMessage().(type) {
	case *livekit.SignalResponse_Offer:
		s.params.Processor.OnOffer(protosignalling.FromProtoSessionDescription(payload.Offer))

	case *livekit.SignalResponse_Answer:
		s.params.Processor.OnAnswer(protosignalling.FromProtoSessionDescription(payload.Answer))
	}

	return nil
}
