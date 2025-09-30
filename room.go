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
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"golang.org/x/exp/maps"
	"golang.org/x/mod/semver"
	"google.golang.org/protobuf/proto"

	protoLogger "github.com/livekit/protocol/logger"
	protosignalling "github.com/livekit/protocol/signalling"
	"github.com/livekit/server-sdk-go/v2/signalling"

	"github.com/livekit/mediatransportutil/pkg/pacer"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

var (
	_ engineHandler = (*Room)(nil)
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

type ConnectOption func(*signalling.ConnectParams)

// WithAutoSubscribe sets whether the participant should automatically subscribe to tracks.
// Default is true.
func WithAutoSubscribe(val bool) ConnectOption {
	return func(p *signalling.ConnectParams) {
		p.AutoSubscribe = val
	}
}

// WithRetransmitBufferSize sets the retransmit buffer size to respond to NACK requests.
// Must be one of: 1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768.
func WithRetransmitBufferSize(val uint16) ConnectOption {
	return func(p *signalling.ConnectParams) {
		p.RetransmitBufferSize = val
	}
}

// WithPacer enables the use of a pacer on this connection
// A pacer helps to smooth out video packet rate to avoid overwhelming downstream. Learn more at: https://chromium.googlesource.com/external/webrtc/+/master/modules/pacing/g3doc/index.md
func WithPacer(pacer pacer.Factory) ConnectOption {
	return func(p *signalling.ConnectParams) {
		p.Pacer = pacer
	}
}

// WithInterceptors sets custom RTP interceptors for the connection.
func WithInterceptors(interceptors []interceptor.Factory) ConnectOption {
	return func(p *signalling.ConnectParams) {
		p.Interceptors = interceptors
	}
}

// WithICETransportPolicy sets the ICE transport policy (UDP, Relay, etc.).
func WithICETransportPolicy(iceTransportPolicy webrtc.ICETransportPolicy) ConnectOption {
	return func(p *signalling.ConnectParams) {
		p.ICETransportPolicy = iceTransportPolicy
	}
}

// WithDisableRegionDiscovery disables automatic region discovery for LiveKit Cloud.
func WithDisableRegionDiscovery() ConnectOption {
	return func(p *signalling.ConnectParams) {
		p.DisableRegionDiscovery = true
	}
}

// WithMetadata sets custom metadata for the participant.
func WithMetadata(metadata string) ConnectOption {
	return func(p *signalling.ConnectParams) {
		p.Metadata = metadata
	}
}

// WithExtraAttributes sets additional key-value attributes for the participant.
// Empty string values will be ignored.
func WithExtraAttributes(attrs map[string]string) ConnectOption {
	return func(p *signalling.ConnectParams) {
		if len(attrs) != 0 && p.Attributes == nil {
			p.Attributes = make(map[string]string, len(attrs))
		}
		for k, v := range attrs {
			if v == "" {
				continue
			}
			p.Attributes[k] = v
		}
	}
}

type PLIWriter func(webrtc.SSRC)

type Room struct {
	log                     protoLogger.Logger
	useSinglePeerConnection bool
	engine                  *RTCEngine
	sid                     string
	name                    string
	LocalParticipant        *LocalParticipant
	callback                *RoomCallback
	connectionState         ConnectionState
	sidReady                chan struct{}

	remoteParticipants map[livekit.ParticipantIdentity]*RemoteParticipant
	sidToIdentity      map[livekit.ParticipantID]livekit.ParticipantIdentity
	sidDefers          map[livekit.ParticipantID]map[livekit.TrackID]func(p *RemoteParticipant)
	metadata           string
	activeSpeakers     []Participant
	serverInfo         *livekit.ServerInfo
	regionURLProvider  *regionURLProvider

	sifTrailer []byte

	byteStreamHandlers *sync.Map
	byteStreamReaders  *sync.Map
	textStreamHandlers *sync.Map
	textStreamReaders  *sync.Map
	rpcHandlers        *sync.Map

	lock sync.RWMutex
}

// NewRoom can be used to update callbacks before calling Join
func NewRoom(callback *RoomCallback) *Room {
	r := &Room{
		log:                     logger,
		useSinglePeerConnection: semver.Compare("v"+Version, "v3.0.0") >= 0,
		remoteParticipants:      make(map[livekit.ParticipantIdentity]*RemoteParticipant),
		sidToIdentity:           make(map[livekit.ParticipantID]livekit.ParticipantIdentity),
		sidDefers:               make(map[livekit.ParticipantID]map[livekit.TrackID]func(*RemoteParticipant)),
		callback:                NewRoomCallback(),
		sidReady:                make(chan struct{}),
		connectionState:         ConnectionStateDisconnected,
		regionURLProvider:       newRegionURLProvider(),
		byteStreamHandlers:      &sync.Map{},
		byteStreamReaders:       &sync.Map{},
		textStreamHandlers:      &sync.Map{},
		textStreamReaders:       &sync.Map{},
		rpcHandlers:             &sync.Map{},
	}
	r.callback.Merge(callback)

	r.engine = NewRTCEngine(r.useSinglePeerConnection, r, r.getLocalParticipantSID)
	r.LocalParticipant = newLocalParticipant(r.engine, r.callback, r.serverInfo)
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

// SID returns the unique session ID of the room.
// This will block until session ID is available, which could take up to 2s after joining the room.
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
	var params signalling.ConnectParams
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
	ctx := context.TODO()

	params := &signalling.ConnectParams{
		AutoSubscribe: true,
	}
	for _, opt := range opts {
		opt(params)
	}

	isSuccess := false
	cloudHostname, _ := parseCloudURL(url)
	if !params.DisableRegionDiscovery && cloudHostname != "" {
		if err := r.regionURLProvider.RefreshRegionSettings(cloudHostname, token); err != nil {
			logger.Errorw("failed to get best url", err)
		} else {
			for tries := uint(0); !isSuccess; tries++ {
				bestURL, err := r.regionURLProvider.PopBestURL(cloudHostname, token)
				if err != nil {
					logger.Errorw("failed to get best url", err)
					break
				}

				logger.Debugw("RTC engine joining room", "url", bestURL)
				// Not exposing this timeout as an option for now so that callers don't
				// set unrealistic values.  We may reconsider in the future though.
				// 4 seconds chosen to balance the trade-offs:
				// - Too long, users will given up.
				// - Too short, risk frequently timing out on a request that would have
				//   succeeded.
				callCtx, cancelCallCtx := context.WithTimeout(ctx, 4*time.Second)
				isSuccess, err = r.engine.JoinContext(callCtx, bestURL, token, params)
				cancelCallCtx()
				if err != nil {
					// try the next URL with exponential backoff
					d := time.Duration(1<<min(tries, 6)) * time.Second // max 64 seconds
					logger.Errorw(
						"failed to join room", err,
						"retrying in", d,
						"url", bestURL,
					)
					time.Sleep(d)
					continue
				}
			}
		}
	}

	if !isSuccess {
		if _, err := r.engine.JoinContext(ctx, url, token, params); err != nil {
			return err
		}
	}

	return nil
}

// Disconnect leaves the room, indicating the client initiated the disconnect.
func (r *Room) Disconnect() {
	r.DisconnectWithReason(livekit.DisconnectReason_CLIENT_INITIATED)
}

// DisconnectWithReason leaves the room with a specific disconnect reason.
func (r *Room) DisconnectWithReason(reason livekit.DisconnectReason) {
	_ = r.engine.SendLeaveWithReason(reason)
	r.cleanup()
}

// ConnectionState returns the current connection state of the room.
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

func (r *Room) deferParticipantUpdate(sid livekit.ParticipantID, trackID livekit.TrackID, fnc func(p *RemoteParticipant)) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if r.sidDefers[sid] == nil {
		r.sidDefers[sid] = make(map[livekit.TrackID]func(p *RemoteParticipant))
	}
	r.sidDefers[sid][trackID] = fnc
}

func (r *Room) runParticipantDefers(sid livekit.ParticipantID, p *RemoteParticipant) {
	r.lock.Lock()
	fncs := r.sidDefers[sid]
	delete(r.sidDefers, sid)
	r.lock.Unlock()

	if len(fncs) != 0 {
		r.log.Infow(
			"running deferred updates for participant",
			"participant", p.Identity(),
			"pID", sid,
			"numUpdates", len(fncs),
		)
		for _, fnc := range fncs {
			fnc(p)
		}
	}
}

func (r *Room) clearParticipantDefers(sid livekit.ParticipantID, pi *livekit.ParticipantInfo) {
	r.lock.Lock()
	defer r.lock.Unlock()

	for trackID := range r.sidDefers[sid] {
		found := false
		for _, ti := range pi.Tracks {
			if livekit.TrackID(ti.GetSid()) == trackID {
				found = true
				break
			}
		}
		if !found {
			r.log.Infow(
				"deleting deferred update for participant",
				"participant", pi.Identity,
				"pID", sid,
				"trackID", trackID,
			)
			delete(r.sidDefers[sid], trackID)
			if len(r.sidDefers[sid]) == 0 {
				delete(r.sidDefers, sid)
			}
		}
	}
}

// GetParticipantByIdentity returns a remote participant by their identity.
// Returns nil if not found.
// Note: this represents the current view from the local participant's perspective
func (r *Room) GetParticipantByIdentity(identity string) *RemoteParticipant {
	r.lock.RLock()
	defer r.lock.RUnlock()

	return r.remoteParticipants[livekit.ParticipantIdentity(identity)]
}

// GetParticipantBySID returns a remote participant by their session ID.
// Returns nil if not found.
// Note: this represents the current view from the local participant's perspective
func (r *Room) GetParticipantBySID(sid string) *RemoteParticipant {
	r.lock.RLock()
	defer r.lock.RUnlock()

	if identity, ok := r.sidToIdentity[livekit.ParticipantID(sid)]; ok {
		return r.remoteParticipants[identity]
	}
	return nil
}

// GetRemoteParticipants returns all remote participants in the room as seen by the local participant
// Note: this does not represent the exact state from the server's view. To get all participants that
// exists on the server, use [RoomServiceClient.ListParticipants] instead.
func (r *Room) GetRemoteParticipants() []*RemoteParticipant {
	r.lock.RLock()
	defer r.lock.RUnlock()

	var participants []*RemoteParticipant
	for _, rp := range r.remoteParticipants {
		participants = append(participants, rp)
	}
	return participants
}

// ActiveSpeakers returns a list of currently active speakers.
// Speakers are ordered by audio level (loudest first).
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

// ServerInfo returns information about the LiveKit server.
func (r *Room) ServerInfo() *livekit.ServerInfo {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return proto.Clone(r.serverInfo).(*livekit.ServerInfo)
}

// SifTrailer returns the SIF (Server Injected Frames) trailer data used by E2EE
func (r *Room) SifTrailer() []byte {
	r.lock.RLock()
	defer r.lock.RUnlock()
	trailer := make([]byte, len(r.sifTrailer))
	copy(trailer, r.sifTrailer)
	return trailer
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

	rp = newRemoteParticipant(pi, r.callback, r.engine, func(ssrc webrtc.SSRC) {
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

func (r *Room) sendSyncState() {
	var previousOffer *webrtc.SessionDescription
	var previousAnswer *webrtc.SessionDescription
	if r.useSinglePeerConnection {
		publisher, ok := r.engine.Publisher()
		if ok {
			previousOffer = publisher.pc.RemoteDescription()
			previousAnswer = publisher.pc.LocalDescription()
		}
	} else {
		subscriber, ok := r.engine.Subscriber()
		if ok {
			previousOffer = subscriber.pc.RemoteDescription()
			previousAnswer = subscriber.pc.LocalDescription()
		}
	}
	if previousOffer == nil || previousAnswer == nil {
		return
	}

	var trackSids []string
	var trackSidsDisabled []string
	sendUnsub := r.engine.connParams.AutoSubscribe
	for _, rp := range r.GetRemoteParticipants() {
		for _, t := range rp.TrackPublications() {
			if t.IsSubscribed() != sendUnsub {
				trackSids = append(trackSids, t.SID())
			}

			if rpub, ok := t.(*RemoteTrackPublication); ok {
				if !rpub.IsEnabled() {
					trackSidsDisabled = append(trackSidsDisabled, t.SID())
				}
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

	r.engine.SendSyncState(&livekit.SyncState{
		Offer:  protosignalling.ToProtoSessionDescription(*previousOffer, 0),
		Answer: protosignalling.ToProtoSessionDescription(*previousAnswer, 0),
		Subscription: &livekit.UpdateSubscription{
			TrackSids: trackSids,
			Subscribe: !sendUnsub,
		},
		PublishTracks:     publishedTracks,
		DataChannels:      dataChannels,
		TrackSidsDisabled: trackSidsDisabled,
		// MIGRATION-TODO DatachannelReceiveStates
	})
}

func (r *Room) cleanup() {
	r.setConnectionState(ConnectionStateDisconnected)
	r.engine.Close()
	r.LocalParticipant.closeTracks()
	r.setSid("", true)
	r.byteStreamHandlers.Clear()
	r.byteStreamReaders.Clear()
	r.textStreamHandlers.Clear()
	r.textStreamReaders.Clear()
	r.rpcHandlers.Clear()
	r.LocalParticipant.cleanup()
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

// Simulate triggers various test scenarios for debugging and testing purposes.
// This is primarily used for development and testing.
func (r *Room) Simulate(scenario SimulateScenario) {
	r.engine.Simulate(scenario)
}

func (r *Room) getLocalParticipantSID() string {
	if r.LocalParticipant != nil {
		return r.LocalParticipant.SID()
	}

	return ""
}

// Establishes the participant as a receiver for calls of the specified RPC method.
// Will overwrite any existing callback for the same method.
//
//   - @param method - The name of the indicated RPC method
//   - @param handler - Will be invoked when an RPC request for this method is received
//   - @returns A promise that resolves when the method is successfully registered
//
// Example:
//
//	room.LocalParticipant?.registerRpcMethod(
//		"greet",
//		func (data: RpcInvocationData) => {
//			fmt.Println("Received greeting from ", data.callerIdentity, "with payload ", data.payload)
//			return "Hello, " + data.callerIdentity + "!";
//		}
//	);
//
// The handler should return either a string or an error.
// If unable to respond within `responseTimeout`, the request will result in an error on the caller's side.
//
// You may throw errors of type `RpcError` with a string `message` in the handler,
// and they will be received on the caller's side with the message intact.
// Other errors thrown in your handler will not be transmitted as-is, and will instead arrive to the caller as `1500` ("Application Error").
func (r *Room) RegisterRpcMethod(method string, handler RpcHandlerFunc) error {
	if _, loaded := r.rpcHandlers.LoadOrStore(method, handler); loaded {
		return fmt.Errorf("rpc handler already registered for method: %s, unregisterRpcMethod before trying to register again", method)
	}
	return nil
}

// UnregisterRpcMethod unregisters a previously registered RPC method.
func (r *Room) UnregisterRpcMethod(method string) {
	r.rpcHandlers.Delete(method)
}

// RegisterTextStreamHandler registers a handler for incoming text streams on a specific topic.
// The handler will be called when a text stream is received for the given topic.
// It returns an error if a handler is already registered for this topic.
func (r *Room) RegisterTextStreamHandler(topic string, handler TextStreamHandler) error {
	if _, loaded := r.textStreamHandlers.LoadOrStore(topic, handler); loaded {
		return fmt.Errorf("text stream handler already registered for topic: %s", topic)
	}
	return nil
}

// UnregisterTextStreamHandler removes a previously registered text stream handler.
func (r *Room) UnregisterTextStreamHandler(topic string) {
	r.textStreamHandlers.Delete(topic)
}

// RegisterByteStreamHandler registers a handler for incoming byte streams on a specific topic.
// The handler will be called when a byte stream is received for the given topic.
// It returns an error if a handler is already registered for this topic.
func (r *Room) RegisterByteStreamHandler(topic string, handler ByteStreamHandler) error {
	if _, loaded := r.byteStreamHandlers.LoadOrStore(topic, handler); loaded {
		return fmt.Errorf("byte stream handler already registered for topic: %s", topic)
	}
	return nil
}

// UnregisterByteStreamHandler removes a previously registered byte stream handler.
func (r *Room) UnregisterByteStreamHandler(topic string) {
	r.byteStreamHandlers.Delete(topic)
}

// engineHandler implementation
func (r *Room) OnMediaTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
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
		r.log.Infow(
			"could not find participant, deferring track update",
			"pID", participantID,
			"trackID", trackID,
			"streamID", streamID,
		)
		r.deferParticipantUpdate(livekit.ParticipantID(participantID), livekit.TrackID(trackID), update)
		return
	}
	update(rp)
	r.runParticipantDefers(livekit.ParticipantID(participantID), rp)
}

func (r *Room) OnRoomJoined(
	room *livekit.Room,
	participant *livekit.ParticipantInfo,
	otherParticipants []*livekit.ParticipantInfo,
	serverInfo *livekit.ServerInfo,
	sifTrailer []byte,
) {
	r.lock.Lock()
	r.name = room.Name
	r.metadata = room.Metadata
	r.serverInfo = serverInfo
	r.connectionState = ConnectionStateConnected
	r.sifTrailer = make([]byte, len(sifTrailer))
	copy(r.sifTrailer, sifTrailer)
	r.lock.Unlock()

	r.setSid(room.Sid, false)

	r.LocalParticipant.updateInfo(participant)
	r.LocalParticipant.updateSubscriptionPermission()

	for _, pi := range otherParticipants {
		r.addRemoteParticipant(pi, true)
		r.clearParticipantDefers(livekit.ParticipantID(pi.Sid), pi)
		// no need to run participant defers here, since we are connected for the first time
	}
}

func (r *Room) OnDisconnected(reason DisconnectionReason) {
	r.callback.OnDisconnected()
	r.callback.OnDisconnectedWithReason(reason)

	r.cleanup()
}

func (r *Room) OnRestarting() {
	r.setConnectionState(ConnectionStateReconnecting)
	r.callback.OnReconnecting()

	for _, rp := range r.GetRemoteParticipants() {
		r.OnParticipantDisconnect(rp)
	}
}

func (r *Room) OnRestarted(
	room *livekit.Room,
	participant *livekit.ParticipantInfo,
	otherParticipants []*livekit.ParticipantInfo,
) {
	r.OnRoomUpdate(room)

	r.LocalParticipant.updateInfo(participant)
	r.LocalParticipant.updateSubscriptionPermission()

	r.OnParticipantUpdate(otherParticipants)

	r.LocalParticipant.republishTracks()

	r.setConnectionState(ConnectionStateConnected)
	r.callback.OnReconnected()
}

func (r *Room) OnResuming() {
	r.setConnectionState(ConnectionStateReconnecting)
	r.callback.OnReconnecting()
}

func (r *Room) OnResumed() {
	r.setConnectionState(ConnectionStateConnected)
	r.callback.OnReconnected()
	r.sendSyncState()
	r.LocalParticipant.updateSubscriptionPermission()
}

func (r *Room) OnDataPacket(identity string, dataPacket DataPacket) {
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

func (r *Room) OnParticipantUpdate(participants []*livekit.ParticipantInfo) {
	for _, pi := range participants {
		if pi.Sid == r.LocalParticipant.SID() || pi.Identity == r.LocalParticipant.Identity() {
			r.LocalParticipant.updateInfo(pi)
			continue
		}

		rp := r.GetParticipantByIdentity(pi.Identity)
		isNew := rp == nil

		if pi.State == livekit.ParticipantInfo_DISCONNECTED {
			r.OnParticipantDisconnect(rp)
		} else if isNew {
			rp = r.addRemoteParticipant(pi, true)
			r.clearParticipantDefers(livekit.ParticipantID(pi.Sid), pi)
			r.runParticipantDefers(livekit.ParticipantID(pi.Sid), rp)
			go r.callback.OnParticipantConnected(rp)
		} else {
			oldSid := livekit.ParticipantID(rp.SID())
			rp.updateInfo(pi)
			newSid := livekit.ParticipantID(rp.SID())
			if oldSid != newSid {
				r.log.Infow(
					"participant sid update",
					"participant", rp.Identity(),
					"sid-old", oldSid,
					"sid-new", newSid,
				)
				r.lock.Lock()
				delete(r.sidDefers, oldSid)
				delete(r.sidToIdentity, oldSid)
				r.sidToIdentity[newSid] = livekit.ParticipantIdentity(rp.Identity())
				r.lock.Unlock()
			}
			r.clearParticipantDefers(livekit.ParticipantID(pi.Sid), pi)
			r.runParticipantDefers(newSid, rp)
		}
	}
}

func (r *Room) OnParticipantDisconnect(rp *RemoteParticipant) {
	if rp == nil {
		return
	}

	r.lock.Lock()
	delete(r.remoteParticipants, livekit.ParticipantIdentity(rp.Identity()))
	delete(r.sidToIdentity, livekit.ParticipantID(rp.SID()))
	delete(r.sidDefers, livekit.ParticipantID(rp.SID()))
	r.lock.Unlock()

	rp.unpublishAllTracks()
	r.LocalParticipant.handleParticipantDisconnected(rp.Identity())
	go r.callback.OnParticipantDisconnected(rp)
}

func (r *Room) OnSpeakersChanged(speakerUpdates []*livekit.SpeakerInfo) {
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

func (r *Room) OnConnectionQuality(updates []*livekit.ConnectionQualityInfo) {
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

func (r *Room) OnRoomUpdate(room *livekit.Room) {
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

func (r *Room) OnRoomMoved(moved *livekit.RoomMovedResponse) {
	r.log.Infow("room moved", "newRoom", moved.Room.Name)
	r.OnRoomUpdate(moved.Room)

	for _, rp := range r.GetRemoteParticipants() {
		r.OnParticipantDisconnect(rp)
	}

	go r.callback.OnRoomMoved(moved.Room.Name, moved.Token)

	infos := make([]*livekit.ParticipantInfo, 0, len(moved.OtherParticipants)+1)
	infos = append(infos, moved.Participant)
	infos = append(infos, moved.OtherParticipants...)
	r.OnParticipantUpdate(infos)
}

func (r *Room) OnTrackRemoteMuted(msg *livekit.MuteTrackRequest) {
	for _, pub := range r.LocalParticipant.TrackPublications() {
		if pub.SID() == msg.Sid {
			localPub := pub.(*LocalTrackPublication)
			// TODO: pause sending data because it'll be dropped by SFU
			localPub.setMuted(msg.Muted, true)
		}
	}
}

func (r *Room) OnLocalTrackUnpublished(msg *livekit.TrackUnpublishedResponse) {
	err := r.LocalParticipant.UnpublishTrack(msg.TrackSid)
	if err != nil {
		r.log.Errorw("could not unpublish track", err, "trackID", msg.TrackSid)
	}
}

func (r *Room) OnTranscription(transcription *livekit.Transcription) {
	var (
		p           Participant
		publication TrackPublication
	)

	if transcription.TranscribedParticipantIdentity == r.LocalParticipant.Identity() {
		p = r.LocalParticipant
		publication = r.LocalParticipant.getPublication(transcription.TrackId)
	} else {
		rp := r.GetParticipantByIdentity(transcription.TranscribedParticipantIdentity)
		if rp == nil {
			r.log.Debugw("recieved transcription for unknown participant", "participant", transcription.TranscribedParticipantIdentity)
			return
		}
		publication = rp.getPublication(transcription.TrackId)
		p = rp
	}
	transcriptionSegments := ExtractTranscriptionSegments(transcription)

	r.callback.OnTranscriptionReceived(transcriptionSegments, p, publication)
}

func (r *Room) OnLocalTrackSubscribed(trackSubscribed *livekit.TrackSubscribed) {
	trackPublication := r.LocalParticipant.getLocalPublication(trackSubscribed.TrackSid)
	if trackPublication == nil {
		r.log.Debugw("recieved track subscribed for unknown track", "trackID", trackSubscribed.TrackSid)
		return
	}
	r.callback.OnLocalTrackSubscribed(trackPublication, r.LocalParticipant)
}

func (r *Room) OnSubscribedQualityUpdate(subscribedQualityUpdate *livekit.SubscribedQualityUpdate) {
	trackPublication := r.LocalParticipant.getLocalPublication(subscribedQualityUpdate.TrackSid)
	if trackPublication == nil {
		r.log.Debugw("recieved subscribed quality update for unknown track", "trackID", subscribedQualityUpdate.TrackSid)
		return
	}

	r.log.Infow(
		"handling subscribed quality update",
		"trackID", trackPublication.SID(),
		"mime", trackPublication.MimeType(),
		"subscribedQualityUpdate", protoLogger.Proto(subscribedQualityUpdate),
	)
	for _, subscribedCodec := range subscribedQualityUpdate.SubscribedCodecs {
		if !strings.HasSuffix(strings.ToLower(trackPublication.MimeType()), subscribedCodec.Codec) {
			continue
		}

		for _, subscribedQuality := range subscribedCodec.Qualities {
			track := trackPublication.GetSimulcastTrack(subscribedQuality.Quality)
			if track != nil {
				track.setMuted(!subscribedQuality.Enabled)
				r.log.Infow(
					"updating layer enable",
					"trackID", trackPublication.SID(),
					"quality", subscribedQuality.Quality,
					"enabled", subscribedQuality.Enabled,
				)
			}
		}
	}
}

func (r *Room) OnMediaSectionsRequirement(mediaSectionsRequirement *livekit.MediaSectionsRequirement) {
	addTransceivers := func(transport *PCTransport, kind webrtc.RTPCodecType, count uint32) {
		for i := uint32(0); i < count; i++ {
			if _, err := transport.PeerConnection().AddTransceiverFromKind(
				kind,
				webrtc.RTPTransceiverInit{
					Direction: webrtc.RTPTransceiverDirectionRecvonly,
				},
			); err != nil {
				r.log.Warnw(
					"could not add transceiver", err,
					"room", r.name,
					"roomID", r.sid,
					"participant", r.LocalParticipant.Identity(),
					"pID", r.LocalParticipant.SID(),
					"kind", kind,
				)
			} else {
				r.log.Debugw(
					"added transceiver of kind",
					"room", r.name,
					"roomID", r.sid,
					"participant", r.LocalParticipant.Identity(),
					"pID", r.LocalParticipant.SID(),
					"kind", kind,
				)
			}
		}
	}

	publisher, ok := r.engine.Publisher()
	if !ok {
		r.log.Warnw("no publisher peer connection", ErrNoPeerConnection)
		return
	}

	addTransceivers(publisher, webrtc.RTPCodecTypeAudio, mediaSectionsRequirement.NumAudios)
	addTransceivers(publisher, webrtc.RTPCodecTypeVideo, mediaSectionsRequirement.NumVideos)
	publisher.Negotiate()
}

func (r *Room) OnStreamHeader(streamHeader *livekit.DataStream_Header, participantIdentity string) {
	switch header := streamHeader.ContentHeader.(type) {
	case *livekit.DataStream_Header_TextHeader:
		streamHandlerCallback, ok := r.textStreamHandlers.Load(streamHeader.Topic)
		if !ok {
			r.log.Debugw("ignoring incoming text stream due to no handler for topic", "topic", streamHeader.Topic)
			return
		}

		info := TextStreamInfo{
			baseStreamInfo: &baseStreamInfo{
				Id:         streamHeader.StreamId,
				MimeType:   streamHeader.MimeType,
				Size:       streamHeader.TotalLength,
				Topic:      streamHeader.Topic,
				Timestamp:  streamHeader.Timestamp,
				Attributes: streamHeader.Attributes,
			},
		}

		textStreamReader := NewTextStreamReader(info, streamHeader.TotalLength)
		r.textStreamReaders.Store(streamHeader.StreamId, textStreamReader)
		go streamHandlerCallback.(TextStreamHandler)(textStreamReader, participantIdentity)
	case *livekit.DataStream_Header_ByteHeader:
		streamHandlerCallback, ok := r.byteStreamHandlers.Load(streamHeader.Topic)
		if !ok {
			r.log.Debugw("ignoring incoming byte stream due to no handler for topic", "topic", streamHeader.Topic)
			return
		}

		info := ByteStreamInfo{
			baseStreamInfo: &baseStreamInfo{
				Id:         streamHeader.StreamId,
				MimeType:   streamHeader.MimeType,
				Size:       streamHeader.TotalLength,
				Topic:      streamHeader.Topic,
				Timestamp:  streamHeader.Timestamp,
				Attributes: streamHeader.Attributes,
			},
			Name: &header.ByteHeader.Name,
		}

		byteStreamReader := NewByteStreamReader(info, streamHeader.TotalLength)
		r.byteStreamReaders.Store(streamHeader.StreamId, byteStreamReader)
		go streamHandlerCallback.(ByteStreamHandler)(byteStreamReader, participantIdentity)
	}
}

func (r *Room) OnStreamChunk(streamChunk *livekit.DataStream_Chunk) {
	streamId := streamChunk.StreamId

	byteStreamReader, ok := r.byteStreamReaders.Load(streamId)
	if ok {
		if len(streamChunk.Content) > 0 {
			byteStreamReader.(*ByteStreamReader).enqueue(streamChunk)
		}
	}

	textStreamReader, ok := r.textStreamReaders.Load(streamId)
	if ok {
		if len(streamChunk.Content) > 0 {
			textStreamReader.(*TextStreamReader).enqueue(streamChunk)
		}
	}
}

func (r *Room) OnStreamTrailer(streamTrailer *livekit.DataStream_Trailer) {
	streamId := streamTrailer.StreamId

	byteStreamReader, ok := r.byteStreamReaders.Load(streamId)
	if ok {
		reader := byteStreamReader.(*ByteStreamReader)
		for k, v := range streamTrailer.Attributes {
			reader.Info.Attributes[k] = v
		}
		reader.close()
		r.byteStreamReaders.Delete(streamId)
	}

	textStreamReader, ok := r.textStreamReaders.Load(streamId)
	if ok {
		reader := textStreamReader.(*TextStreamReader)
		for k, v := range streamTrailer.Attributes {
			reader.Info.Attributes[k] = v
		}
		reader.close()
		r.textStreamReaders.Delete(streamId)
	}
}

func (r *Room) OnRpcRequest(callerIdentity, requestId, method, payload string, responseTimeout time.Duration, version uint32) {
	r.engine.publishRpcAck(callerIdentity, requestId)

	if version != 1 {
		r.engine.publishRpcResponse(callerIdentity, requestId, nil, rpcErrorFromBuiltInCodes(RpcUnsupportedVersion, nil))
		return
	}

	handler, ok := r.rpcHandlers.Load(method)
	if !ok {
		r.engine.publishRpcResponse(callerIdentity, requestId, nil, rpcErrorFromBuiltInCodes(RpcUnsupportedMethod, nil))
		return
	}

	response, err := handler.(RpcHandlerFunc)(RpcInvocationData{
		RequestID:       requestId,
		CallerIdentity:  callerIdentity,
		Payload:         payload,
		ResponseTimeout: responseTimeout,
	})

	if err != nil {
		if _, ok := err.(*RpcError); ok {
			r.engine.publishRpcResponse(callerIdentity, requestId, nil, err.(*RpcError))
		} else {
			r.log.Warnw("unexpected error returned by RPC handler for method, using application error instead", err, "method", method)
			r.engine.publishRpcResponse(callerIdentity, requestId, nil, rpcErrorFromBuiltInCodes(RpcApplicationError, nil))
		}
		return
	}

	if byteLength(response) > MaxDataBytes {
		r.engine.publishRpcResponse(callerIdentity, requestId, nil, rpcErrorFromBuiltInCodes(RpcResponsePayloadTooLarge, nil))
		return
	}

	r.engine.publishRpcResponse(callerIdentity, requestId, &response, nil)
}

func (r *Room) OnRpcAck(requestId string) {
	r.LocalParticipant.HandleIncomingRpcAck(requestId)
}

func (r *Room) OnRpcResponse(requestId string, payload *string, error *RpcError) {
	r.LocalParticipant.HandleIncomingRpcResponse(requestId, payload, error)
}

// ---------------------------------------------------------

func unpackStreamID(packed string) (participantId string, trackId string) {
	parts := strings.Split(packed, "|")
	if len(parts) > 1 {
		return parts[0], packed[len(parts[0])+1:]
	}
	return packed, ""
}
