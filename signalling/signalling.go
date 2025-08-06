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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	protosignalling "github.com/livekit/protocol/signalling"
	"github.com/pion/webrtc/v4"
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

func (s *signalling) SetLogger(l logger.Logger) {
	s.params.Logger = l
}

func (s *signalling) Path() string {
	return "/rtc"
}

func (s *signalling) ValidatePath() string {
	return "/rtc/validate"
}

func (s *signalling) ConnectQueryParams(
	version string,
	protocol int,
	connectParams *ConnectParams,
	addTrackRequests []*livekit.AddTrackRequest,
	publisherOffer webrtc.SessionDescription,
	participantSID string,
) (string, error) {
	queryParams := fmt.Sprintf("version=%s&protocol=%d&", version, protocol)

	if connectParams.AutoSubscribe {
		queryParams += "&auto_subscribe=1"
	} else {
		queryParams += "&auto_subscribe=0"
	}
	if connectParams.Reconnect {
		queryParams += "&reconnect=1"
		if participantSID != "" {
			queryParams += fmt.Sprintf("&sid=%s", participantSID)
		}
	}
	if len(connectParams.Attributes) != 0 {
		data, err := json.Marshal(connectParams.Attributes)
		if err != nil {
			return "", ErrInvalidParameter
		}
		str := base64.URLEncoding.EncodeToString(data)
		queryParams += "&attributes=" + str
	}
	queryParams += "&sdk=go&os=" + runtime.GOOS

	// add JoinRequest as base64 encoded protobuf bytes
	clientInfo := &livekit.ClientInfo{
		Version:  version,
		Protocol: int32(protocol),
		Os:       runtime.GOOS,
		Sdk:      livekit.ClientInfo_GO,
	}

	connectionSettings := &livekit.ConnectionSettings{
		AutoSubscribe: connectParams.AutoSubscribe,
	}

	joinRequest := &livekit.JoinRequest{
		ClientInfo:            clientInfo,
		ConnectionSettings:    connectionSettings,
		Metadata:              connectParams.Metadata,
		ParticipantAttributes: connectParams.Attributes,
		AddTrackRequests:      addTrackRequests,
		PublisherOffer:        protosignalling.ToProtoSessionDescription(publisherOffer, 0),
	}
	if connectParams.Reconnect {
		joinRequest.Reconnect = true
		if participantSID != "" {
			joinRequest.ParticipantSid = participantSID
		}
	}

	marshalled, err := proto.Marshal(joinRequest)
	if err != nil {
		return "", err
	}

	queryParams += fmt.Sprintf("&join_request=%s", base64.URLEncoding.EncodeToString(marshalled))
	return queryParams, nil
}

func (s *signalling) HTTPRequestForValidate(
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

	queryParams, err := s.ConnectQueryParams(
		version,
		protocol,
		connectParams,
		nil,
		webrtc.SessionDescription{},
		participantSID,
	)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(ToHttpURL(urlPrefix) + s.ValidatePath() + fmt.Sprintf("?%s", queryParams))
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		s.params.Logger.Errorw("error creating validate request", err)
		return nil, err
	}
	req.Header = NewHTTPHeaderWithToken(token)
	return req, nil
}

func (s *signalling) DecodeErrorResponse(errorDetails []byte) string {
	return string(errorDetails)
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

func (s *signalling) SignalSimulateScenario(simulate *livekit.SimulateScenario) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_Simulate{
			Simulate: simulate,
		},
	}
}

func (s *signalling) SignalMuteTrack(mute *livekit.MuteTrackRequest) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_Mute{
			Mute: mute,
		},
	}
}

func (s *signalling) SignalUpdateSubscription(updateSubscription *livekit.UpdateSubscription) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_Subscription{
			Subscription: updateSubscription,
		},
	}
}

func (s *signalling) SignalSyncState(syncState *livekit.SyncState) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_SyncState{
			SyncState: syncState,
		},
	}
}

func (s *signalling) SignalAddTrack(addTrack *livekit.AddTrackRequest) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: addTrack,
		},
	}
}

func (s *signalling) SignalSubscriptionPermission(subscriptionPermission *livekit.SubscriptionPermission) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_SubscriptionPermission{
			SubscriptionPermission: subscriptionPermission,
		},
	}
}

func (s *signalling) SignalUpdateTrackSettings(settings *livekit.UpdateTrackSettings) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_TrackSetting{
			TrackSetting: settings,
		},
	}
}

func (s *signalling) SignalUpdateParticipantMetadata(metadata *livekit.UpdateParticipantMetadata) proto.Message {
	return &livekit.SignalRequest{
		Message: &livekit.SignalRequest_UpdateMetadata{
			UpdateMetadata: metadata,
		},
	}
}
