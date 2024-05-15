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
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/pion/webrtc/v3"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/mediatransportutil"
	"github.com/livekit/protocol/livekit"
)

const (
	trackPublishTimeout = 10 * time.Second
	timeSyncTimeout     = 5 * time.Second
)

type LocalParticipant struct {
	baseParticipant
	engine *RTCEngine

	timeSyncLock         sync.Mutex
	timeSynchronized     *core.Fuse
	timeSyncResponseChan chan livekit.TimeSyncResponse
	timeSyncInfo         *mediatransportutil.TimeSyncInfo
}

func newLocalParticipant(engine *RTCEngine, roomcallback *RoomCallback) *LocalParticipant {
	return &LocalParticipant{
		baseParticipant:      *newBaseParticipant(roomcallback),
		engine:               engine,
		timeSyncResponseChan: make(chan livekit.TimeSyncResponse, 10),
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
	transceiver, err := publisher.PeerConnection().AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	})
	if err != nil {
		return nil, err
	}

	// LocalTrack will consume rtcp packets so we don't need to consume again
	_, isSampleTrack := track.(*LocalTrack)
	pub.setSender(transceiver.Sender(), !isSampleTrack)

	pub.updateInfo(pubRes.Track)
	p.addPublication(pub)

	publisher.Negotiate()

	logger.Infow("published track", "name", opts.Name, "source", opts.Source.String(), "trackID", pubRes.Track.Sid)

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

	logger.Infow("published simulcast track", "name", opts.Name, "source", opts.Source.String(), "trackID", pubRes.Track.Sid)

	return pub, nil
}

func (p *LocalParticipant) republishTracksAndResetTimeSync() {
	p.timeSyncLock.Lock()
	p.timeSyncInfo = nil
	p.timeSyncLock.Unlock()

	var localPubs []*LocalTrackPublication
	p.tracks.Range(func(key, value interface{}) bool {
		track := value.(*LocalTrackPublication)

		if track.Track() != nil || len(track.simulcastTracks) > 0 {
			localPubs = append(localPubs, track)
		}
		p.tracks.Delete(key)
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
			logger.Warnw("could not republish track as no track local found", nil, "track", pub.SID())
		}
	}
}

func (p *LocalParticipant) closeTracks() {
	var localPubs []*LocalTrackPublication
	p.tracks.Range(func(_, value interface{}) bool {
		track := value.(*LocalTrackPublication)
		if track.Track() != nil || len(track.simulcastTracks) > 0 {
			localPubs = append(localPubs, track)
		}
		return true
	})

	for _, pub := range localPubs {
		pub.CloseTrack()
	}
}

func (p *LocalParticipant) syncTime(timeSyncInfo *mediatransportutil.TimeSyncInfo) {
	req := p.timeSyncInfo.StartTimeSync()
	dataPacket := &livekit.DataPacket{Value: &livekit.DataPacket_TimeSyncRequest{
		TimeSyncRequest: &req,
	}}

	err := p.publishData(livekit.DataPacket_RELIABLE, dataPacket)
	if err != nil {
		logger.Debugw("time sync data channel request failed sending", "error", err)
		return
	}

	for {
		select {
		case <-time.After(timeSyncTimeout):
			logger.Debugw("timeout waiting for time synchronization response")
			return
		case resp := <-p.timeSyncResponseChan:
			err := timeSyncInfo.HandleTimeSyncResponse(resp)
			if err == nil {
				logger.Debugw("time synchronization successful")
				return
			}
			// Wait for a potential other response to come on the channel
			logger.Debugw("error handling time synchronization response", "error", err)
		}
	}
}

func (p *LocalParticipant) getSynchronizedTimeSync() *mediatransportutil.TimeSyncInfo {
	needToSyncTime := false
	p.timeSyncLock.Lock()
	if p.timeSyncInfo == nil {
		needToSyncTime = true
		p.timeSyncInfo = &mediatransportutil.TimeSyncInfo{}
		p.timeSynchronized = &core.Fuse{}
	}

	timeSyncInfo := p.timeSyncInfo
	timeSynchronized := p.timeSynchronized

	p.timeSyncLock.Unlock()

	// Do not hold the lock while synchronizing time to prevent blocking the reconnection callback that resets timeSyncInfo
	if needToSyncTime {
		// Do not retry time sync if it fails for now as the most likely reason is that the SFU does not support the time sync protocol
		p.syncTime(timeSyncInfo)
		timeSynchronized.Break()
	}

	fmt.Println("time synced", p.timeSyncInfo)

	<-timeSynchronized.Watch()

	return timeSyncInfo
}

func (p *LocalParticipant) convertLocalTimeToSFUTime(ts *uint64, timeSyncInfo *mediatransportutil.TimeSyncInfo) {
	if ts == nil {
		return
	}

	timeTs := mediatransportutil.NtpTime(*ts).Time()

	sfuTs, err := p.timeSyncInfo.GetPeerTimeForLocalTime(timeTs)
	if err != nil {
		// Leave time as is
		return
	}
	ntpSfuTs := mediatransportutil.ToNtpTime(sfuTs)

	*ts = uint64(ntpSfuTs)
}

func (p *LocalParticipant) publishData(kind livekit.DataPacket_Kind, dataPacket *livekit.DataPacket) error {
	if err := p.engine.ensurePublisherConnected(true); err != nil {
		return err
	}

	if userPkt, ok := dataPacket.Value.(*livekit.DataPacket_User); ok {
		if userPkt.User.StartTime != nil || userPkt.User.StartTime != nil {
			timeSyncInfo := p.getSynchronizedTimeSync()

			p.convertLocalTimeToSFUTime(userPkt.User.StartTime, timeSyncInfo)
			p.convertLocalTimeToSFUTime(userPkt.User.EndTime, timeSyncInfo)
		}
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
	_ DataPacket = (*livekit.TimeSyncResponse)(nil)
)

// UserData is a custom user data that can be sent via WebRTC.
func UserData(data []byte) *UserDataPacket {
	return &UserDataPacket{Payload: data}
}

// UserDataPacket is a custom user data that can be sent via WebRTC on a custom topic.
type UserDataPacket struct {
	Payload   []byte
	Topic     string     // optional
	StartTime *time.Time // optional
	EndTime   *time.Time // optional
}

// ToProto implements DataPacket.
func (p *UserDataPacket) ToProto() *livekit.DataPacket {
	var topic *string
	if p.Topic != "" {
		topic = proto.String(p.Topic)
	}

	var startTime, endTime *uint64
	if p.StartTime != nil {
		startTime = proto.Uint64(uint64(mediatransportutil.ToNtpTime(*p.StartTime)))
	}
	if p.EndTime != nil {
		endTime = proto.Uint64(uint64(mediatransportutil.ToNtpTime(*p.EndTime)))
	}

	return &livekit.DataPacket{Value: &livekit.DataPacket_User{
		User: &livekit.UserPacket{
			Payload:   p.Payload,
			Topic:     topic,
			StartTime: startTime,
			EndTime:   endTime,
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

	logger.Infow("unpublished track", "name", pub.Name(), "sid", sid)

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
// updates will be performed only if the participant has canUpdateOwnMetadata grant
func (p *LocalParticipant) SetMetadata(metadata string) {
	_ = p.engine.client.SendUpdateParticipantMetadata(&livekit.UpdateParticipantMetadata{
		Metadata: metadata,
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

func (p *LocalParticipant) onTimeSyncResponse(resp livekit.TimeSyncResponse) {
	select {
	case p.timeSyncResponseChan <- resp:
	default:
		logger.Infow("time sync response chan full")
	}
}
