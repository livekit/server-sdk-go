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
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"google.golang.org/protobuf/proto"
)

var _ Signalling = (*signalling)(nil)

type SignallingParams struct {
	Logger logger.Logger
}

type signalling struct {
	signallingUnimplemented

	params SignallingParams
}

func NewSignalling(params SignallingParams) Signalling {
	return &signalling{
		params: params,
	}
}

func (s *signalling) SignalLeaveRequest(leave *livekit.LeaveRequest) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_Leave{
			Leave: leave,
		},
	}
}

func (s *signalling) SignalICECandidate(trickle *livekit.TrickleRequest) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_Trickle{
			Trickle: trickle,
		},
	}
}

func (s *signalling) SignalSdpOffer(offer *livekit.SessionDescription) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_Offer{
			Offer: offer,
		},
	}
}

func (s *signalling) SignalSdpAnswer(answer *livekit.SessionDescription) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_Answer{
			Answer: answer,
		},
	}
}
