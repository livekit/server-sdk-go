package lksdk

import (
	"github.com/livekit/protocol/livekit"
	"github.com/pion/webrtc/v3"
)

type ParticipantCallback struct {
	// for all participants
	OnTrackMuted               func(pub TrackPublication, p Participant)
	OnTrackUnmuted             func(pub TrackPublication, p Participant)
	OnMetadataChanged          func(oldMetadata string, p Participant)
	OnIsSpeakingChanged        func(p Participant)
	OnConnectionQualityChanged func(update *livekit.ConnectionQualityInfo, p Participant)

	// for remote participants
	OnTrackSubscribed         func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant)
	OnTrackUnsubscribed       func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant)
	OnTrackSubscriptionFailed func(sid string, rp *RemoteParticipant)
	OnTrackPublished          func(publication *RemoteTrackPublication, rp *RemoteParticipant)
	OnTrackUnpublished        func(publication *RemoteTrackPublication, rp *RemoteParticipant)
	OnDataReceived            func(data []byte, rp *RemoteParticipant)
}

func NewParticipantCallback() *ParticipantCallback {
	return &ParticipantCallback{
		OnTrackMuted:               func(pub TrackPublication, p Participant) {},
		OnTrackUnmuted:             func(pub TrackPublication, p Participant) {},
		OnMetadataChanged:          func(oldMetadata string, p Participant) {},
		OnIsSpeakingChanged:        func(p Participant) {},
		OnConnectionQualityChanged: func(update *livekit.ConnectionQualityInfo, p Participant) {},
		OnTrackSubscribed:          func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {},
		OnTrackUnsubscribed:        func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {},
		OnTrackSubscriptionFailed:  func(sid string, rp *RemoteParticipant) {},
		OnTrackPublished:           func(publication *RemoteTrackPublication, rp *RemoteParticipant) {},
		OnTrackUnpublished:         func(publication *RemoteTrackPublication, rp *RemoteParticipant) {},
		OnDataReceived:             func(data []byte, rp *RemoteParticipant) {},
	}
}

type RoomCallback struct {
	OnDisconnected            func()
	OnParticipantConnected    func(*RemoteParticipant)
	OnParticipantDisconnected func(*RemoteParticipant)
	OnActiveSpeakersChanged   func([]Participant)
	OnRoomMetadataChanged     func(metadata string)

	// participant events are sent to the room as well
	ParticipantCallback
}

func NewRoomCallback() *RoomCallback {
	pc := NewParticipantCallback()
	return &RoomCallback{
		ParticipantCallback: *pc,

		OnDisconnected:            func() {},
		OnParticipantConnected:    func(participant *RemoteParticipant) {},
		OnParticipantDisconnected: func(participant *RemoteParticipant) {},
		OnActiveSpeakersChanged:   func(participants []Participant) {},
		OnRoomMetadataChanged:     func(metadata string) {},
	}
}
