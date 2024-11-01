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

package lksdk

import (
	"sort"
	"time"

	"github.com/pion/webrtc/v3"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
)

const (
	trackPublishTimeout = 10 * time.Second
)

type LocalParticipant struct {
	baseParticipant
	engine                 *RTCEngine
	subscriptionPermission *livekit.SubscriptionPermission
}

func newLocalParticipant(engine *RTCEngine, roomcallback *RoomCallback) *LocalParticipant {
	return &LocalParticipant{
		baseParticipant: *newBaseParticipant(roomcallback),
		engine:          engine,
	}
}

func (p *LocalParticipant) PublishTrack(track webrtc.TrackLocal, opts *TrackPublicationOptions) (*LocalTrackPublication, error) {
	if opts == nil {
		opts = &TrackPublicationOptions{}
	}
	kind := KindFromRTPType(track.Kind())
	// default sources, since clients generally look for camera/mic
	if opts.Source == livekit.TrackSource_UNKNOWN {
		if kind == TrackKindVideo {
			opts.Source = livekit.TrackSource_CAMERA
		} else if kind == TrackKindAudio {
			opts.Source = livekit.TrackSource_MICROPHONE
		}
	}

	publisher, ok := p.engine.Publisher()
	if !ok {
		return nil, ErrNoPeerConnection
	}

	pub := NewLocalTrackPublication(kind, track, *opts, p.engine.client)
	pub.onMuteChanged = p.onTrackMuted

	req := &livekit.AddTrackRequest{
		Cid:        track.ID(),
		Name:       opts.Name,
		Source:     opts.Source,
		Type:       kind.ProtoType(),
		Width:      uint32(opts.VideoWidth),
		Height:     uint32(opts.VideoHeight),
		DisableDtx: opts.DisableDTX,
		Stereo:     opts.Stereo,
		Stream:     opts.Stream,
	}
	if kind == TrackKindVideo {
		// single layer
		req.Layers = []*livekit.VideoLayer{
			{
				Quality: livekit.VideoQuality_HIGH,
				Width:   uint32(opts.VideoWidth),
				Height:  uint32(opts.VideoHeight),
			},
		}
	}
	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: req,
		},
	})
	if err != nil {
		return nil, err
	}

	// add transceivers
	transceiver, err := publisher.PeerConnection().AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	})
	if err != nil {
		return nil, err
	}

	// LocalTrack will consume rtcp packets so we don't need to consume again
	_, isSampleTrack := track.(*LocalTrack)
	pub.setSender(transceiver.Sender(), !isSampleTrack)

	publisher.Negotiate()

	pubChan := p.engine.TrackPublishedChan()
	var pubRes *livekit.TrackPublishedResponse

	select {
	case pubRes = <-pubChan:
		break
	case <-time.After(trackPublishTimeout):
		return nil, ErrTrackPublishTimeout
	}

	pub.updateInfo(pubRes.Track)
	p.addPublication(pub)

	p.Callback.OnLocalTrackPublished(pub, p)
	p.roomCallback.OnLocalTrackPublished(pub, p)

	p.engine.log.Infow("published track", "name", opts.Name, "source", opts.Source.String(), "trackID", pubRes.Track.Sid)

	return pub, nil
}

// PublishSimulcastTrack publishes up to three layers to the server
func (p *LocalParticipant) PublishSimulcastTrack(tracks []*LocalTrack, opts *TrackPublicationOptions) (*LocalTrackPublication, error) {
	if len(tracks) == 0 {
		return nil, nil
	}

	for _, track := range tracks {
		if track.Kind() != webrtc.RTPCodecTypeVideo {
			return nil, ErrUnsupportedSimulcastKind
		}
		if track.videoLayer == nil || track.RID() == "" {
			return nil, ErrInvalidSimulcastTrack
		}
	}

	tracksCopy := make([]*LocalTrack, len(tracks))
	copy(tracksCopy, tracks)

	// tracks should be low to high
	sort.Slice(tracksCopy, func(i, j int) bool {
		return tracksCopy[i].videoLayer.Width < tracksCopy[j].videoLayer.Width
	})

	if opts == nil {
		opts = &TrackPublicationOptions{}
	}
	// default sources, since clients generally look for camera/mic
	if opts.Source == livekit.TrackSource_UNKNOWN {
		opts.Source = livekit.TrackSource_CAMERA
	}

	mainTrack := tracksCopy[len(tracksCopy)-1]

	pub := NewLocalTrackPublication(KindFromRTPType(mainTrack.Kind()), nil, *opts, p.engine.client)
	pub.onMuteChanged = p.onTrackMuted

	var layers []*livekit.VideoLayer
	for _, st := range tracksCopy {
		layers = append(layers, st.videoLayer)
	}
	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: &livekit.AddTrackRequest{
				Cid:    mainTrack.ID(),
				Name:   opts.Name,
				Source: opts.Source,
				Type:   pub.Kind().ProtoType(),
				Width:  mainTrack.videoLayer.Width,
				Height: mainTrack.videoLayer.Height,
				Layers: layers,
				SimulcastCodecs: []*livekit.SimulcastCodec{
					{
						Codec: mainTrack.Codec().MimeType,
						Cid:   mainTrack.ID(),
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	pubChan := p.engine.TrackPublishedChan()
	var pubRes *livekit.TrackPublishedResponse

	select {
	case pubRes = <-pubChan:
		break
	case <-time.After(trackPublishTimeout):
		return nil, ErrTrackPublishTimeout
	}

	publisher, ok := p.engine.Publisher()
	if !ok {
		return nil, ErrNoPeerConnection
	}

	// add transceivers
	publishPC := publisher.PeerConnection()
	var transceiver *webrtc.RTPTransceiver
	var sender *webrtc.RTPSender
	for idx, st := range tracksCopy {
		if idx == 0 {
			transceiver, err = publishPC.AddTransceiverFromTrack(st, webrtc.RTPTransceiverInit{
				Direction: webrtc.RTPTransceiverDirectionSendonly,
			})
			if err != nil {
				return nil, err
			}
			sender = transceiver.Sender()
			pub.setSender(sender, false)
		} else {
			if err = sender.AddEncoding(st); err != nil {
				return nil, err
			}
		}
		pub.addSimulcastTrack(st)
		st.SetTransceiver(transceiver)
	}

	pub.updateInfo(pubRes.Track)
	p.addPublication(pub)

	publisher.Negotiate()

	p.Callback.OnLocalTrackPublished(pub, p)
	p.roomCallback.OnLocalTrackPublished(pub, p)

	p.engine.log.Infow("published simulcast track", "name", opts.Name, "source", opts.Source.String(), "trackID", pubRes.Track.Sid)

	return pub, nil
}

func (p *LocalParticipant) republishTracks() {
	var localPubs []*LocalTrackPublication
	p.tracks.Range(func(key, value interface{}) bool {
		track := value.(*LocalTrackPublication)

		if track.Track() != nil || len(track.simulcastTracks) > 0 {
			localPubs = append(localPubs, track)
		}
		p.tracks.Delete(key)
		p.audioTracks.Delete(key)
		p.videoTracks.Delete(key)

		p.Callback.OnLocalTrackUnpublished(track, p)
		p.roomCallback.OnLocalTrackUnpublished(track, p)
		return true
	})

	for _, pub := range localPubs {
		opt := pub.PublicationOptions()
		if len(pub.simulcastTracks) > 0 {
			var tracks []*LocalTrack
			for _, st := range pub.simulcastTracks {
				tracks = append(tracks, st)
			}
			p.PublishSimulcastTrack(tracks, &opt)
		} else if track := pub.TrackLocal(); track != nil {
			p.PublishTrack(track, &opt)
		} else {
			p.engine.log.Warnw("could not republish track as no track local found", nil, "track", pub.SID())
		}
	}
}

func (p *LocalParticipant) closeTracks() {
	var localPubs []*LocalTrackPublication
	p.tracks.Range(func(key, value interface{}) bool {
		track := value.(*LocalTrackPublication)
		if track.Track() != nil || len(track.simulcastTracks) > 0 {
			localPubs = append(localPubs, track)
		}
		p.tracks.Delete(key)
		p.audioTracks.Delete(key)
		p.videoTracks.Delete(key)
		return true
	})

	for _, pub := range localPubs {
		pub.CloseTrack()
	}
}

func (p *LocalParticipant) publishData(kind livekit.DataPacket_Kind, dataPacket *livekit.DataPacket) error {
	if err := p.engine.ensurePublisherConnected(true); err != nil {
		return err
	}

	encoded, err := proto.Marshal(dataPacket)
	if err != nil {
		return err
	}

	return p.engine.GetDataChannel(kind).Send(encoded)
}

// PublishData sends custom user data via WebRTC data channel.
//
// By default, the message can be received by all participants in a room,
// see WithDataPublishDestination for choosing specific participants.
//
// Messages are sent via a LOSSY channel by default, see WithDataPublishReliable for sending reliable data.
//
// Deprecated: Use PublishDataPacket with UserData instead.
func (p *LocalParticipant) PublishData(payload []byte, opts ...DataPublishOption) error {
	options := &dataPublishOptions{}
	for _, opt := range opts {
		opt(options)
	}
	return p.PublishDataPacket(UserData(payload), opts...)
}

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

// PublishDataPacket sends a packet via a WebRTC data channel. UserData can be used for sending custom user data.
//
// By default, the message can be received by all participants in a room,
// see WithDataPublishDestination for choosing specific participants.
//
// Messages are sent via UDP and offer no delivery guarantees, see WithDataPublishReliable for sending data reliably (with retries).
func (p *LocalParticipant) PublishDataPacket(pck DataPacket, opts ...DataPublishOption) error {
	options := &dataPublishOptions{}
	for _, opt := range opts {
		opt(options)
	}
	dataPacket := pck.ToProto()
	if options.Topic != "" {
		if u, ok := dataPacket.Value.(*livekit.DataPacket_User); ok && u.User != nil {
			u.User.Topic = proto.String(options.Topic)
		}
	}

	// This matches the default value of Kind on protobuf level.
	kind := livekit.DataPacket_LOSSY
	if options.Reliable != nil && *options.Reliable {
		kind = livekit.DataPacket_RELIABLE
	}
	//lint:ignore SA1019 backward compatibility
	dataPacket.Kind = kind

	dataPacket.DestinationIdentities = options.DestinationIdentities
	if u, ok := dataPacket.Value.(*livekit.DataPacket_User); ok && u.User != nil {
		//lint:ignore SA1019 backward compatibility
		u.User.DestinationIdentities = options.DestinationIdentities
	}

	return p.publishData(kind, dataPacket)
}

func (p *LocalParticipant) UnpublishTrack(sid string) error {
	obj, loaded := p.tracks.LoadAndDelete(sid)
	if !loaded {
		return ErrCannotFindTrack
	}
	p.audioTracks.Delete(sid)
	p.videoTracks.Delete(sid)

	pub, ok := obj.(*LocalTrackPublication)
	if !ok {
		return nil
	}

	var err error
	if localTrack, ok := pub.track.(webrtc.TrackLocal); ok {
		publisher, ok := p.engine.Publisher()
		if !ok {
			return ErrNoPeerConnection
		}
		for _, sender := range publisher.pc.GetSenders() {
			if sender.Track() == localTrack {
				err = publisher.pc.RemoveTrack(sender)
				break
			}
		}
		publisher.Negotiate()
	}

	pub.CloseTrack()

	p.Callback.OnLocalTrackUnpublished(pub, p)
	p.roomCallback.OnLocalTrackUnpublished(pub, p)

	p.engine.log.Infow("unpublished track", "name", pub.Name(), "sid", sid)

	return err
}

// GetSubscriberPeerConnection is a power-user API that gives access to the underlying subscriber peer connection
// subscribed tracks are received using this PeerConnection
func (p *LocalParticipant) GetSubscriberPeerConnection() *webrtc.PeerConnection {
	if subscriber, ok := p.engine.Subscriber(); ok {
		return subscriber.PeerConnection()
	}
	return nil
}

// GetPublisherPeerConnection is a power-user API that gives access to the underlying publisher peer connection
// local tracks are published to server via this PeerConnection
func (p *LocalParticipant) GetPublisherPeerConnection() *webrtc.PeerConnection {
	if publisher, ok := p.engine.Publisher(); ok {
		return publisher.PeerConnection()
	}
	return nil
}

// SetName sets the name of the current participant.
// updates will be performed only if the participant has canUpdateOwnMetadata grant
func (p *LocalParticipant) SetName(name string) {
	_ = p.engine.client.SendUpdateParticipantMetadata(&livekit.UpdateParticipantMetadata{
		Name: name,
	})
}

// SetMetadata sets the metadata of the current participant.
// Updates will be performed only if the participant has canUpdateOwnMetadata grant.
func (p *LocalParticipant) SetMetadata(metadata string) {
	_ = p.engine.client.SendUpdateParticipantMetadata(&livekit.UpdateParticipantMetadata{
		Metadata: metadata,
	})
}

// SetAttributes sets the KV attributes of the current participant.
// To remove an attribute, set it to empty value.
// Updates will be performed only if the participant has canUpdateOwnMetadata grant.
func (p *LocalParticipant) SetAttributes(attrs map[string]string) {
	_ = p.engine.client.SendUpdateParticipantMetadata(&livekit.UpdateParticipantMetadata{
		Attributes: attrs,
	})
}

func (p *LocalParticipant) updateInfo(info *livekit.ParticipantInfo) {
	p.baseParticipant.updateInfo(info, p)

	// detect tracks that have been muted remotely, and apply changes
	for _, ti := range info.Tracks {
		pub := p.getLocalPublication(ti.Sid)
		if pub == nil {
			continue
		}
		if pub.IsMuted() != ti.Muted {
			_ = p.engine.client.SendMuteTrack(pub.SID(), pub.IsMuted())
		}
	}
}

func (p *LocalParticipant) getLocalPublication(sid string) *LocalTrackPublication {
	if pub, ok := p.getPublication(sid).(*LocalTrackPublication); ok {
		return pub
	}
	return nil
}

func (p *LocalParticipant) onTrackMuted(pub *LocalTrackPublication, muted bool) {
	if muted {
		p.Callback.OnTrackMuted(pub, p)
		p.roomCallback.OnTrackMuted(pub, p)
	} else {
		p.Callback.OnTrackUnmuted(pub, p)
		p.roomCallback.OnTrackUnmuted(pub, p)
	}
}

// Control who can subscribe to LocalParticipant's published tracks.
//
// By default, all participants can subscribe. This allows fine-grained control over
// who is able to subscribe at a participant and track level.
//
// Note: if access is given at a track-level (i.e. both `AllParticipants` and
// `TrackPermission.AllTracks` are false), any newer published tracks
// will not grant permissions to any participants and will require a subsequent
// permissions update to allow subscription.
func (p *LocalParticipant) SetSubscriptionPermission(sp *livekit.SubscriptionPermission) {
	p.lock.Lock()
	p.subscriptionPermission = proto.Clone(sp).(*livekit.SubscriptionPermission)
	p.updateSubscriptionPermissionLocked()
	p.lock.Unlock()
}

func (p *LocalParticipant) updateSubscriptionPermission() {
	p.lock.RLock()
	defer p.lock.RUnlock()

	p.updateSubscriptionPermissionLocked()
}

func (p *LocalParticipant) updateSubscriptionPermissionLocked() {
	if p.subscriptionPermission == nil {
		return
	}

	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_SubscriptionPermission{
			SubscriptionPermission: p.subscriptionPermission,
		},
	})
	if err != nil {
		logger.Errorw(
			"could not send subscription permission", err,
			"participant", p.identity,
			"pID", p.sid,
		)
	}
}
