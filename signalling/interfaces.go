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

	"github.com/livekit/mediatransportutil/pkg/pacer"
	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"
)

type Signalling interface {
	SetLogger(l protoLogger.Logger)

	SignalLeaveRequest(leave *livekit.LeaveRequest) proto.Message
	SignalICECandidate(trickle *livekit.TrickleRequest) proto.Message
	SignalSdpOffer(offer *livekit.SessionDescription) proto.Message
	SignalSdpAnswer(answer *livekit.SessionDescription) proto.Message
	SignalSimulateScenario(simulate *livekit.SimulateScenario) proto.Message
	SignalMuteTrack(mute *livekit.MuteTrackRequest) proto.Message
	SignalUpdateSubscription(updateSubscription *livekit.UpdateSubscription) proto.Message
	SignalSyncState(syncState *livekit.SyncState) proto.Message
	SignalAddTrack(addTrack *livekit.AddTrackRequest) proto.Message
	SignalSubscriptionPermission(subscriptionPermission *livekit.SubscriptionPermission) proto.Message
	SignalUpdateTrackSettings(settings *livekit.UpdateTrackSettings) proto.Message
	SignalUpdateParticipantMetadata(metadata *livekit.UpdateParticipantMetadata) proto.Message

	AckMessageId(ackMessageId uint32)
	SetLastProcessedRemoteMessageId(lastProcessedRemoteMessageId uint32)
}

type ConnectParams struct {
	AutoSubscribe          bool
	Reconnect              bool
	DisableRegionDiscovery bool

	RetransmitBufferSize uint16

	Attributes map[string]string // See WithExtraAttributes

	Pacer pacer.Factory

	Interceptors []interceptor.Factory

	ICETransportPolicy webrtc.ICETransportPolicy
}

type SignalTransport interface {
	SetLogger(l protoLogger.Logger)

	Start()
	IsStarted() bool
	Close()
	Join(
		ctx context.Context,
		url string,
		token string,
		connectParams ConnectParams,
	) error
	SendMessage(msg proto.Message) error
}

type SignalTransportHandler interface {
	OnTransportClose()
}

type SignalHandler interface {
	SetLogger(l protoLogger.Logger)

	HandleMessage(msg proto.Message) error
}

type SignalProcessor interface {
	OnJoinResponse(joinResponse *livekit.JoinResponse)
	OnAnswer(sd webrtc.SessionDescription, answerId uint32)
	OnOffer(sd webrtc.SessionDescription, offerId uint32)
	OnTrickle(init webrtc.ICECandidateInit, target livekit.SignalTarget)
	OnParticipantUpdate([]*livekit.ParticipantInfo)
	OnLocalTrackPublished(response *livekit.TrackPublishedResponse)
	OnLocalTrackUnpublished(response *livekit.TrackUnpublishedResponse)
	OnSpeakersChanged([]*livekit.SpeakerInfo)
	OnConnectionQuality([]*livekit.ConnectionQualityInfo)
	OnRoomUpdate(room *livekit.Room)
	OnRoomMoved(moved *livekit.RoomMovedResponse)
	OnTrackRemoteMuted(request *livekit.MuteTrackRequest)
	OnTokenRefresh(refreshToken string)
	OnLeave(*livekit.LeaveRequest)
	OnLocalTrackSubscribed(trackSubscribed *livekit.TrackSubscribed)
	OnSubscribedQualityUpdate(subscribedQualityUpdate *livekit.SubscribedQualityUpdate)
}
