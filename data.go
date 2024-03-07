package lksdk

type dataPublishOptions struct {
	Reliable              *bool
	DestinationIdentities []string
	Topic                 string
}

type DataReceiveParams struct {
	Sender         *RemoteParticipant
	SenderIdentity string
	Topic          string // Deprecated: Use UserDataPacket.Topic
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
