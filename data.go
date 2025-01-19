package lksdk

import (
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

// Data types

type DataPacket interface {
	ToProto() *livekit.DataPacket
}

// Compile-time assertion for all supported data packet types.
var (
	_ DataPacket = (*UserDataPacket)(nil)
	_ DataPacket = (*livekit.SipDTMF)(nil) // implemented in the protocol package
)

// UserData is a custom user data that can be sent via WebRTC.
func UserData(data []byte) *UserDataPacket {
	return &UserDataPacket{Payload: data}
}

// UserDataPacket is a custom user data that can be sent via WebRTC on a custom topic.
type UserDataPacket struct {
	Payload []byte
	Topic   string // optional
}

// ToProto implements DataPacket.
func (p *UserDataPacket) ToProto() *livekit.DataPacket {
	var topic *string
	if p.Topic != "" {
		topic = proto.String(p.Topic)
	}
	return &livekit.DataPacket{Value: &livekit.DataPacket_User{
		User: &livekit.UserPacket{
			Payload: p.Payload,
			Topic:   topic,
		},
	}}
}

// receiving

type DataReceiveParams struct {
	Sender         *RemoteParticipant
	SenderIdentity string
	Topic          string // Deprecated: Use UserDataPacket.Topic
}

// publishing

type dataPublishOptions struct {
	Reliable              *bool
	DestinationIdentities []string
	Topic                 string
}

type DataPublishOption func(*dataPublishOptions)

func WithDataPublishTopic(topic string) DataPublishOption {
	return func(o *dataPublishOptions) {
		o.Topic = topic
	}
}

func WithDataPublishReliable(reliable bool) DataPublishOption {
	return func(o *dataPublishOptions) {
		o.Reliable = &reliable
	}
}

func WithDataPublishDestination(identities []string) DataPublishOption {
	return func(o *dataPublishOptions) {
		o.DestinationIdentities = identities
	}
}
