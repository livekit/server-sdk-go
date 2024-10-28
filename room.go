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
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"golang.org/x/exp/maps"
	"google.golang.org/protobuf/proto"

	protoLogger "github.com/livekit/protocol/logger"

	"github.com/livekit/mediatransportutil/pkg/pacer"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

// -----------------------------------------------

type SimulateScenario int

const (
	SimulateSignalReconnect SimulateScenario = iota
	SimulateForceTCP
	SimulateForceTLS
	SimulateSpeakerUpdate
	SimulateMigration
	SimulateServerLeave
	SimulateNodeFailure
)

type ConnectionState string

const (
	ConnectionStateConnected    ConnectionState = "connected"
	ConnectionStateReconnecting ConnectionState = "reconnecting"
	ConnectionStateDisconnected ConnectionState = "disconnected"
)

// -----------------------------------------------

const (
	SimulateSpeakerUpdateInterval = 5
)

type (
	TrackPubCallback func(track Track, pub TrackPublication, participant *RemoteParticipant)
	PubCallback      func(pub TrackPublication, participant *RemoteParticipant)
)

type ParticipantKind int

const (
	ParticipantStandard = ParticipantKind(livekit.ParticipantInfo_STANDARD)
	ParticipantIngress  = ParticipantKind(livekit.ParticipantInfo_INGRESS)
	ParticipantEgress   = ParticipantKind(livekit.ParticipantInfo_EGRESS)
	ParticipantSIP      = ParticipantKind(livekit.ParticipantInfo_SIP)
	ParticipantAgent    = ParticipantKind(livekit.ParticipantInfo_AGENT)
)

type ConnectInfo struct {
	APIKey                string
	APISecret             string
	RoomName              string
	ParticipantName       string
	ParticipantIdentity   string
	ParticipantKind       ParticipantKind
	ParticipantMetadata   string
	ParticipantAttributes map[string]string
}

type RTPHeaderExtensionConfig struct {
	Audio []webrtc.RTPHeaderExtensionCapability
	Video []webrtc.RTPHeaderExtensionCapability
}

// not exposed to users. clients should use ConnectOption
type connectParams struct {
	AutoSubscribe          bool
	Reconnect              bool
	DisableRegionDiscovery bool

	PublisherHeaderExtensions  RTPHeaderExtensionConfig
	SubscriberHeaderExtensions RTPHeaderExtensionConfig

	RetransmitBufferSize uint16

	Pacer pacer.Factory

	Interceptors []interceptor.Factory

	ICETransportPolicy webrtc.ICETransportPolicy
}

type ConnectOption func(*connectParams)

func WithAutoSubscribe(val bool) ConnectOption {
	return func(p *connectParams) {
		p.AutoSubscribe = val
	}
}

// Retransmit buffer size to reponse to nack request,
// must be one of: 1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768
func WithRetransmitBufferSize(val uint16) ConnectOption {
	return func(p *connectParams) {
		p.RetransmitBufferSize = val
	}
}

// WithPacer enables the use of a pacer on this connection
// A pacer helps to smooth out video packet rate to avoid overwhelming downstream. Learn more at: https://chromium.googlesource.com/external/webrtc/+/master/modules/pacing/g3doc/index.md
func WithPacer(pacer pacer.Factory) ConnectOption {
	return func(p *connectParams) {
		p.Pacer = pacer
	}
}

func WithInterceptors(interceptors []interceptor.Factory) ConnectOption {
	return func(p *connectParams) {
		p.Interceptors = interceptors
	}
}

func WithICETransportPolicy(iceTransportPolicy webrtc.ICETransportPolicy) ConnectOption {
	return func(p *connectParams) {
		p.ICETransportPolicy = iceTransportPolicy
	}
}

func WithDisableRegionDiscovery() ConnectOption {
	return func(p *connectParams) {
		p.DisableRegionDiscovery = true
	}
}

func WithPublisherAudioRTPHeaderExtensions(uris []string) ConnectOption {
	return func(p *connectParams) {
		for _, uri := range uris {
			p.PublisherHeaderExtensions.Audio = append(p.PublisherHeaderExtensions.Audio, webrtc.RTPHeaderExtensionCapability{URI: uri})
		}
	}
}

func WithPublisherVideoRTPHeaderExtensions(uris []string) ConnectOption {
	return func(p *connectParams) {
		for _, uri := range uris {
			p.PublisherHeaderExtensions.Video = append(p.PublisherHeaderExtensions.Video, webrtc.RTPHeaderExtensionCapability{URI: uri})
		}
	}
}

func WithSubscriberAudioRTPHeaderExtensions(uris []string) ConnectOption {
	return func(p *connectParams) {
		for _, uri := range uris {
			p.SubscriberHeaderExtensions.Audio = append(p.SubscriberHeaderExtensions.Audio, webrtc.RTPHeaderExtensionCapability{URI: uri})
		}
	}
}

func WithSubscriberVideoRTPHeaderExtensions(uris []string) ConnectOption {
	return func(p *connectParams) {
		for _, uri := range uris {
			p.SubscriberHeaderExtensions.Video = append(p.SubscriberHeaderExtensions.Audio, webrtc.RTPHeaderExtensionCapability{URI: uri})
		}
	}
}

type PLIWriter func(webrtc.SSRC)

type Room struct {
	log              protoLogger.Logger
	engine           *RTCEngine
	sid              string
	name             string
	LocalParticipant *LocalParticipant
	callback         *RoomCallback
	connectionState  ConnectionState
	sidReady         chan struct{}

	remoteParticipants map[livekit.ParticipantIdentity]*RemoteParticipant
	sidToIdentity      map[livekit.ParticipantID]livekit.ParticipantIdentity
	sidDefers          map[livekit.ParticipantID][]func(p *RemoteParticipant)
	metadata           string
	activeSpeakers     []Participant
	serverInfo         *livekit.ServerInfo
	regionURLProvider  *regionURLProvider

	lock sync.RWMutex
}

// NewRoom can be used to update callbacks before calling Join
func NewRoom(callback *RoomCallback) *Room {
	engine := NewRTCEngine()
	r := &Room{
		log:                logger,
		engine:             engine,
		remoteParticipants: make(map[livekit.ParticipantIdentity]*RemoteParticipant),
		sidToIdentity:      make(map[livekit.ParticipantID]livekit.ParticipantIdentity),
		sidDefers:          make(map[livekit.ParticipantID][]func(*RemoteParticipant)),
		callback:           NewRoomCallback(),
		sidReady:           make(chan struct{}),
		connectionState:    ConnectionStateDisconnected,
		regionURLProvider:  newRegionURLProvider(),
	}
	r.callback.Merge(callback)
	r.LocalParticipant = newLocalParticipant(engine, r.callback)

	// callbacks from engine
	engine.OnMediaTrack = r.handleMediaTrack
	engine.OnDisconnected = r.handleDisconnect
	engine.OnParticipantUpdate = r.handleParticipantUpdate
	engine.OnSpeakersChanged = r.handleSpeakersChange
	engine.OnDataPacket = r.handleDataReceived
	engine.OnConnectionQuality = r.handleConnectionQualityUpdate
	engine.OnRoomUpdate = r.handleRoomUpdate
	engine.OnRestarting = r.handleRestarting
	engine.OnRestarted = r.handleRestarted
	engine.OnResuming = r.handleResuming
	engine.OnResumed = r.handleResumed
	engine.client.OnLocalTrackUnpublished = r.handleLocalTrackUnpublished
	engine.client.OnTrackRemoteMuted = r.handleTrackRemoteMuted

	return r
}

// ConnectToRoom creates and joins the room
func ConnectToRoom(url string, info ConnectInfo, callback *RoomCallback, opts ...ConnectOption) (*Room, error) {
	room := NewRoom(callback)
	err := room.Join(url, info, opts...)
	if err != nil {
		return nil, err
	}
	return room, nil
}

// ConnectToRoomWithToken creates and joins the room
func ConnectToRoomWithToken(url, token string, callback *RoomCallback, opts ...ConnectOption) (*Room, error) {
	room := NewRoom(callback)
	err := room.JoinWithToken(url, token, opts...)
	if err != nil {
		return nil, err
	}
	return room, nil
}

// SetLogger overrides default logger.
func (r *Room) SetLogger(l protoLogger.Logger) {
	r.log = l
	r.engine.SetLogger(l)
}

func (r *Room) Name() string {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.name
}

func (r *Room) SID() string {
	<-r.sidReady
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.sid
}

// PrepareConnection - with LiveKit Cloud, determine the best edge data center for the current client to connect to
func (r *Room) PrepareConnection(url, token string) error {
	cloudHostname, _ := parseCloudURL(url)
	if cloudHostname == "" {
		return nil
	}

	return r.regionURLProvider.RefreshRegionSettings(cloudHostname, token)
}

// Join - joins the room as with default permissions
func (r *Room) Join(url string, info ConnectInfo, opts ...ConnectOption) error {
	var params connectParams
	for _, opt := range opts {
		opt(&params)
	}

	// generate token
	at := auth.NewAccessToken(info.APIKey, info.APISecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     info.RoomName,
	}
	at.SetVideoGrant(grant).
		SetIdentity(info.ParticipantIdentity).
		SetMetadata(info.ParticipantMetadata).
		SetAttributes(info.ParticipantAttributes).
		SetName(info.ParticipantName).
		SetKind(livekit.ParticipantInfo_Kind(info.ParticipantKind))

	token, err := at.ToJWT()
	if err != nil {
		return err
	}

	return r.JoinWithToken(url, token, opts...)
}

// JoinWithToken - customize participant options by generating your own token
func (r *Room) JoinWithToken(url, token string, opts ...ConnectOption) error {
	params := &connectParams{
		AutoSubscribe: true,
	}
	for _, opt := range opts {
		opt(params)
	}

	var joinRes *livekit.JoinResponse
	cloudHostname, _ := parseCloudURL(url)
	if !params.DisableRegionDiscovery && cloudHostname != "" {
		if err := r.regionURLProvider.RefreshRegionSettings(cloudHostname, token); err != nil {
			logger.Errorw("failed to get best url", err)
		} else {
			for tries := uint(0); joinRes == nil; tries++ {
				bestURL, err := r.regionURLProvider.PopBestURL(cloudHostname, token)
				if err != nil {
					logger.Errorw("failed to get best url", err)
					break
				}

				logger.Debugw("RTC engine joining room",
					"url", bestURL,
				)
				joinRes, err = r.engine.Join(bestURL, token, params)
				if err != nil {
					// try the next URL with exponential backoff
					d := time.Duration(1<<min(tries, 6)) * time.Second // max 64 seconds
					logger.Errorw("failed to join room", err,
						"retrying in", d,
					)
					time.Sleep(d)
					continue
				}
			}
		}
	}

	if joinRes == nil {
		var err error
		joinRes, err = r.engine.Join(url, token, params)
		if err != nil {
			return err
		}
	}

	r.lock.Lock()
	r.name = joinRes.Room.Name
	r.metadata = joinRes.Room.Metadata
	r.serverInfo = joinRes.ServerInfo
	r.connectionState = ConnectionStateConnected
	r.lock.Unlock()

	r.setSid(joinRes.Room.Sid, false)

	r.LocalParticipant.updateInfo(joinRes.Participant)
	r.LocalParticipant.updateSubscriptionPermission()

	for _, pi := range joinRes.OtherParticipants {
		r.addRemoteParticipant(pi, true)
	}

	return nil
}

func (r *Room) Disconnect() {
	r.DisconnectWithReason(livekit.DisconnectReason_UNKNOWN_REASON)
}

func (r *Room) DisconnectWithReason(reason livekit.DisconnectReason) {
	_ = r.engine.client.SendLeaveWithReason(reason)
	r.cleanup()
}

func (r *Room) ConnectionState() ConnectionState {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.connectionState
}

func (r *Room) setConnectionState(cs ConnectionState) {
	r.lock.Lock()
	r.connectionState = cs
	r.lock.Unlock()
}

func (r *Room) deferParticipantUpdate(sid livekit.ParticipantID, fnc func(p *RemoteParticipant)) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.sidDefers[sid] = append(r.sidDefers[sid], fnc)
}

func (r *Room) runParticipantDefers(sid livekit.ParticipantID, p *RemoteParticipant) {
	r.lock.RLock()
	has := len(r.sidDefers[sid]) != 0
	r.lock.RUnlock()
	if !has {
		return
	}
	r.lock.Lock()
	fncs := r.sidDefers[sid]
	delete(r.sidDefers, sid)
	r.lock.Unlock()
	if len(fncs) == 0 {
		return
	}
	r.log.Infow("running deferred updates for participant", "participantID", sid, "updates", len(fncs))
	for _, fnc := range fncs {
		fnc(p)
	}
}

func (r *Room) GetParticipantByIdentity(identity string) *RemoteParticipant {
	r.lock.RLock()
	defer r.lock.RUnlock()

	return r.remoteParticipants[livekit.ParticipantIdentity(identity)]
}

func (r *Room) GetParticipantBySID(sid string) *RemoteParticipant {
	r.lock.RLock()
	defer r.lock.RUnlock()

	if identity, ok := r.sidToIdentity[livekit.ParticipantID(sid)]; ok {
		return r.remoteParticipants[identity]
	}
	return nil
}

func (r *Room) GetRemoteParticipants() []*RemoteParticipant {
	r.lock.RLock()
	defer r.lock.RUnlock()

	var participants []*RemoteParticipant
	for _, rp := range r.remoteParticipants {
		participants = append(participants, rp)
	}
	return participants
}

func (r *Room) ActiveSpeakers() []Participant {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.activeSpeakers
}

func (r *Room) Metadata() string {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.metadata
}

func (r *Room) ServerInfo() *livekit.ServerInfo {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return proto.Clone(r.serverInfo).(*livekit.ServerInfo)
}

func (r *Room) addRemoteParticipant(pi *livekit.ParticipantInfo, updateExisting bool) *RemoteParticipant {
	r.lock.Lock()
	defer r.lock.Unlock()
	rp, ok := r.remoteParticipants[livekit.ParticipantIdentity(pi.Identity)]
	if ok {
		if updateExisting {
			rp.updateInfo(pi)
			r.sidToIdentity[livekit.ParticipantID(pi.Sid)] = livekit.ParticipantIdentity(pi.Identity)
		}

		return rp
	}

	rp = newRemoteParticipant(pi, r.callback, r.engine.client, func(ssrc webrtc.SSRC) {
		pli := []rtcp.Packet{
			&rtcp.PictureLossIndication{SenderSSRC: uint32(ssrc), MediaSSRC: uint32(ssrc)},
		}
		if subscriber, ok := r.engine.Subscriber(); ok {
			_ = subscriber.pc.WriteRTCP(pli)
		}
	})
	r.remoteParticipants[livekit.ParticipantIdentity(pi.Identity)] = rp
	r.sidToIdentity[livekit.ParticipantID(pi.Sid)] = livekit.ParticipantIdentity(pi.Identity)
	return rp
}

func (r *Room) handleMediaTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	// ensure we have the participant
	participantID, streamID := unpackStreamID(track.StreamID())
	trackID := track.ID()

	if strings.HasPrefix(streamID, "TR_") {
		// backwards compatibility
		trackID = streamID
	}
	update := func(p *RemoteParticipant) {
		p.addSubscribedMediaTrack(track, trackID, receiver)
	}

	rp := r.GetParticipantBySID(participantID)
	if rp == nil {
		r.log.Infow("could not find participant, deferring track update", "participantID", participantID)
		r.deferParticipantUpdate(livekit.ParticipantID(participantID), update)
		return
	}
	update(rp)
}

func (r *Room) handleDisconnect(reason DisconnectionReason) {
	r.callback.OnDisconnected()
	r.callback.OnDisconnectedWithReason(reason)

	r.cleanup()
}

func (r *Room) handleRestarting() {
	r.setConnectionState(ConnectionStateReconnecting)
	r.callback.OnReconnecting()

	for _, rp := range r.GetRemoteParticipants() {
		r.handleParticipantDisconnect(rp)
	}
}

func (r *Room) handleRestarted(joinRes *livekit.JoinResponse) {
	r.handleRoomUpdate(joinRes.Room)

	r.LocalParticipant.updateInfo(joinRes.Participant)
	r.LocalParticipant.updateSubscriptionPermission()

	r.handleParticipantUpdate(joinRes.OtherParticipants)

	r.LocalParticipant.republishTracks()

	r.setConnectionState(ConnectionStateConnected)
	r.callback.OnReconnected()
}

func (r *Room) handleResuming() {
	r.setConnectionState(ConnectionStateReconnecting)
	r.callback.OnReconnecting()
}

func (r *Room) handleResumed() {
	r.setConnectionState(ConnectionStateConnected)
	r.callback.OnReconnected()
	r.sendSyncState()
	r.LocalParticipant.updateSubscriptionPermission()
}

func (r *Room) handleDataReceived(identity string, dataPacket DataPacket) {
	if identity == r.LocalParticipant.Identity() {
		// if sent by itself, do not handle data
		return
	}
	p := r.GetParticipantByIdentity(identity)
	params := DataReceiveParams{
		SenderIdentity: identity,
		Sender:         p,
	}
	switch msg := dataPacket.(type) {
	case *UserDataPacket: // compatibility
		params.Topic = msg.Topic
		if p != nil {
			p.Callback.OnDataReceived(msg.Payload, params)
		}
		r.callback.OnDataReceived(msg.Payload, params)
	}
	if p != nil {
		p.Callback.OnDataPacket(dataPacket, params)
	}
	r.callback.OnDataPacket(dataPacket, params)
}

func (r *Room) handleParticipantUpdate(participants []*livekit.ParticipantInfo) {
	for _, pi := range participants {
		if pi.Sid == r.LocalParticipant.SID() || pi.Identity == r.LocalParticipant.Identity() {
			r.LocalParticipant.updateInfo(pi)
			continue
		}

		rp := r.GetParticipantByIdentity(pi.Identity)
		isNew := rp == nil

		if pi.State == livekit.ParticipantInfo_DISCONNECTED {
			// remove
			if rp != nil {
				r.handleParticipantDisconnect(rp)
			}
		} else if isNew {
			rp = r.addRemoteParticipant(pi, true)
			go r.callback.OnParticipantConnected(rp)
		} else {
			oldSid := livekit.ParticipantID(rp.SID())
			rp.updateInfo(pi)
			newSid := livekit.ParticipantID(rp.SID())
			if oldSid != newSid {
				r.log.Infow("participant sid update", "sid-old", oldSid, "sid-new", newSid, "identity", rp.Identity())
				r.lock.Lock()
				delete(r.sidToIdentity, oldSid)
				r.sidToIdentity[newSid] = livekit.ParticipantIdentity(rp.Identity())
				r.lock.Unlock()
				r.runParticipantDefers(newSid, rp)
			}
		}
	}
}

func (r *Room) handleParticipantDisconnect(p *RemoteParticipant) {
	r.lock.Lock()
	delete(r.remoteParticipants, livekit.ParticipantIdentity(p.Identity()))
	delete(r.sidToIdentity, livekit.ParticipantID(p.SID()))
	delete(r.sidDefers, livekit.ParticipantID(p.SID()))
	r.lock.Unlock()

	p.unpublishAllTracks()
	go r.callback.OnParticipantDisconnected(p)
}

func (r *Room) handleSpeakersChange(speakerUpdates []*livekit.SpeakerInfo) {
	speakerMap := make(map[string]Participant)
	for _, p := range r.ActiveSpeakers() {
		speakerMap[p.SID()] = p
	}
	for _, info := range speakerUpdates {
		var participant Participant
		if info.Sid == r.LocalParticipant.SID() {
			participant = r.LocalParticipant
		} else {
			participant = r.GetParticipantBySID(info.Sid)
		}
		if reflect.ValueOf(participant).IsNil() {
			continue
		}

		participant.setAudioLevel(info.Level)
		participant.setIsSpeaking(info.Active)

		if info.Active {
			speakerMap[info.Sid] = participant
		} else {
			delete(speakerMap, info.Sid)
		}
	}

	activeSpeakers := maps.Values(speakerMap)
	sort.Slice(activeSpeakers, func(i, j int) bool {
		return activeSpeakers[i].AudioLevel() > activeSpeakers[j].AudioLevel()
	})
	r.lock.Lock()
	r.activeSpeakers = activeSpeakers
	r.lock.Unlock()
	go r.callback.OnActiveSpeakersChanged(activeSpeakers)
}

func (r *Room) handleConnectionQualityUpdate(updates []*livekit.ConnectionQualityInfo) {
	for _, update := range updates {
		if update.ParticipantSid == r.LocalParticipant.SID() {
			r.LocalParticipant.setConnectionQualityInfo(update)
		} else {
			p := r.GetParticipantBySID(update.ParticipantSid)
			if p != nil {
				p.setConnectionQualityInfo(update)
			} else {
				r.log.Debugw("could not find participant", "sid", update.ParticipantSid,
					"localParticipant", r.LocalParticipant.SID())
			}
		}
	}
}

func (r *Room) handleRoomUpdate(room *livekit.Room) {
	metadataChanged := false
	r.lock.Lock()
	if r.metadata != room.Metadata {
		metadataChanged = true
		r.metadata = room.Metadata
	}
	r.lock.Unlock()
	r.setSid(room.Sid, false)
	if metadataChanged {
		go r.callback.OnRoomMetadataChanged(room.Metadata)
	}
}

func (r *Room) handleTrackRemoteMuted(msg *livekit.MuteTrackRequest) {
	for _, pub := range r.LocalParticipant.TrackPublications() {
		if pub.SID() == msg.Sid {
			localPub := pub.(*LocalTrackPublication)
			// TODO: pause sending data because it'll be dropped by SFU
			localPub.setMuted(msg.Muted, true)
		}
	}
}

func (r *Room) handleLocalTrackUnpublished(msg *livekit.TrackUnpublishedResponse) {
	err := r.LocalParticipant.UnpublishTrack(msg.TrackSid)
	if err != nil {
		r.log.Errorw("could not unpublish track", err, "trackID", msg.TrackSid)
	}
}

func (r *Room) sendSyncState() {
	subscriber, ok := r.engine.Subscriber()
	if !ok || subscriber.pc.RemoteDescription() == nil {
		return
	}

	previousSdp := subscriber.pc.LocalDescription()

	var trackSids []string
	sendUnsub := r.engine.connParams.AutoSubscribe
	for _, rp := range r.GetRemoteParticipants() {
		for _, t := range rp.TrackPublications() {
			if t.IsSubscribed() != sendUnsub {
				trackSids = append(trackSids, t.SID())
			}
		}
	}

	var publishedTracks []*livekit.TrackPublishedResponse
	for _, t := range r.LocalParticipant.TrackPublications() {
		if t.Track() != nil {
			publishedTracks = append(publishedTracks, &livekit.TrackPublishedResponse{
				Cid:   t.Track().ID(),
				Track: t.TrackInfo(),
			})
		}
	}

	var dataChannels []*livekit.DataChannelInfo
	getDCinfo := func(dc *webrtc.DataChannel, target livekit.SignalTarget) {
		if dc != nil && dc.ID() != nil {
			dataChannels = append(dataChannels, &livekit.DataChannelInfo{
				Label:  dc.Label(),
				Id:     uint32(*dc.ID()),
				Target: target,
			})
		}
	}

	getDCinfo(r.engine.GetDataChannel(livekit.DataPacket_RELIABLE), livekit.SignalTarget_PUBLISHER)
	getDCinfo(r.engine.GetDataChannel(livekit.DataPacket_LOSSY), livekit.SignalTarget_PUBLISHER)
	getDCinfo(r.engine.GetDataChannelSub(livekit.DataPacket_RELIABLE), livekit.SignalTarget_SUBSCRIBER)
	getDCinfo(r.engine.GetDataChannelSub(livekit.DataPacket_LOSSY), livekit.SignalTarget_SUBSCRIBER)

	r.engine.client.SendSyncState(&livekit.SyncState{
		Answer: ToProtoSessionDescription(*previousSdp),
		Subscription: &livekit.UpdateSubscription{
			TrackSids: trackSids,
			Subscribe: !sendUnsub,
		},
		PublishTracks: publishedTracks,
		DataChannels:  dataChannels,
	})
}

func (r *Room) cleanup() {
	r.setConnectionState(ConnectionStateDisconnected)
	r.engine.Close()
	r.LocalParticipant.closeTracks()
	r.setSid("", true)
}

func (r *Room) setSid(sid string, allowEmpty bool) {
	r.lock.Lock()
	if sid != "" || allowEmpty {
		select {
		case <-r.sidReady:
		// already closed
		default:
			r.sid = sid
			close(r.sidReady)
		}
	}
	r.lock.Unlock()
}

func (r *Room) Simulate(scenario SimulateScenario) {
	switch scenario {
	case SimulateSignalReconnect:
		r.engine.client.Close()
	case SimulateForceTCP:
		// pion does not support active tcp candidate, skip
	case SimulateForceTLS:
		req := &livekit.SignalRequest{
			Message: &livekit.SignalRequest_Simulate{
				Simulate: &livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_SwitchCandidateProtocol{
						SwitchCandidateProtocol: livekit.CandidateProtocol_TLS,
					},
				},
			},
		}
		r.engine.client.SendRequest(req)
		r.engine.client.OnLeave(&livekit.LeaveRequest{CanReconnect: true, Reason: livekit.DisconnectReason_CLIENT_INITIATED})
	case SimulateSpeakerUpdate:
		r.engine.client.SendRequest(&livekit.SignalRequest{
			Message: &livekit.SignalRequest_Simulate{
				Simulate: &livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_SpeakerUpdate{
						SpeakerUpdate: SimulateSpeakerUpdateInterval,
					},
				},
			},
		})
	case SimulateMigration:
		r.engine.client.SendRequest(&livekit.SignalRequest{
			Message: &livekit.SignalRequest_Simulate{
				Simulate: &livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_Migration{
						Migration: true,
					},
				},
			},
		})
	case SimulateServerLeave:
		r.engine.client.SendRequest(&livekit.SignalRequest{
			Message: &livekit.SignalRequest_Simulate{
				Simulate: &livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_ServerLeave{
						ServerLeave: true,
					},
				},
			},
		})
	case SimulateNodeFailure:
		r.engine.client.SendRequest(&livekit.SignalRequest{
			Message: &livekit.SignalRequest_Simulate{
				Simulate: &livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_NodeFailure{
						NodeFailure: true,
					},
				},
			},
		})
	}
}

func unpackStreamID(packed string) (participantId string, trackId string) {
	parts := strings.Split(packed, "|")
	if len(parts) > 1 {
		return parts[0], packed[len(parts[0])+1:]
	}
	return packed, ""
}
