package lksdk

import (
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/thoas/go-funk"
)

type SimulateScenario int

const (
	SimulateSignalReconnect SimulateScenario = iota
)

type TrackPubCallback func(track Track, pub TrackPublication, participant *RemoteParticipant)
type PubCallback func(pub TrackPublication, participant *RemoteParticipant)

type ConnectInfo struct {
	APIKey              string
	APISecret           string
	RoomName            string
	ParticipantName     string
	ParticipantIdentity string
	ParticipantMetadata string
}

type ConnectParams struct {
	AutoSubscribe bool
	Reconnect     bool
	Callback      *RoomCallback
}

type ConnectOption func(*ConnectParams)

func WithAutoSubscribe(val bool) ConnectOption {
	return func(p *ConnectParams) {
		p.AutoSubscribe = val
	}
}

type PLIWriter func(webrtc.SSRC)

type Room struct {
	engine           *RTCEngine
	sid              string
	name             string
	LocalParticipant *LocalParticipant
	callback         *RoomCallback

	participants   map[string]*RemoteParticipant
	metadata       string
	activeSpeakers []Participant

	lock sync.RWMutex
}

// CreateRoom can be used to update callbacks before calling Join
func CreateRoom(callback *RoomCallback) *Room {
	engine := NewRTCEngine()
	r := &Room{
		engine:       engine,
		participants: make(map[string]*RemoteParticipant),
		callback:     NewRoomCallback(),
	}
	r.callback.Merge(callback)
	r.LocalParticipant = newLocalParticipant(engine, r.callback)

	// callbacks from engine
	engine.OnMediaTrack = r.handleMediaTrack
	engine.OnDisconnected = r.handleDisconnect
	engine.OnParticipantUpdate = r.handleParticipantUpdate
	engine.OnActiveSpeakersChanged = r.handleActiveSpeakerChange
	engine.OnSpeakersChanged = r.handleSpeakersChange
	engine.OnDataReceived = r.handleDataReceived
	engine.OnConnectionQuality = r.handleConnectionQualityUpdate
	engine.OnRoomUpdate = r.handleRoomUpdate
	engine.OnRestarting = r.handleRestarting
	engine.OnRestarted = r.handleRestarted
	engine.OnResuming = r.handleResuming
	engine.OnResumed = r.handleResumed
	engine.client.OnLocalTrackUnpublished = r.handleLocalTrackUnpublished
	engine.client.OnTrackMuted = r.handleTrackMuted

	return r
}

// ConnectToRoom creates and joins the room
func ConnectToRoom(url string, info ConnectInfo, callback *RoomCallback, opts ...ConnectOption) (*Room, error) {
	room := CreateRoom(callback)
	err := room.Join(url, info, opts...)
	if err != nil {
		return nil, err
	}
	return room, nil
}

// ConnectToRoomWithToken creates and joins the room
func ConnectToRoomWithToken(url, token string, callback *RoomCallback, opts ...ConnectOption) (*Room, error) {
	room := CreateRoom(callback)
	err := room.JoinWithToken(url, token, opts...)
	if err != nil {
		return nil, err
	}
	return room, nil
}

func (r *Room) Name() string {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.name
}

func (r *Room) SID() string {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.sid
}

// Join should only be used with CreateRoom
func (r *Room) Join(url string, info ConnectInfo, opts ...ConnectOption) error {
	var params ConnectParams
	for _, opt := range opts {
		opt(&params)
	}

	if params.Callback != nil {
		r.callback.Merge(params.Callback)
	}

	// generate token
	at := auth.NewAccessToken(info.APIKey, info.APISecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     info.RoomName,
	}
	at.AddGrant(grant).
		SetIdentity(info.ParticipantIdentity).
		SetMetadata(info.ParticipantMetadata).
		SetName(info.ParticipantName)

	token, err := at.ToJWT()
	if err != nil {
		return err
	}

	return r.JoinWithToken(url, token, opts...)
}

// JoinWithToken should only be used with CreateRoom
func (r *Room) JoinWithToken(url, token string, opts ...ConnectOption) error {
	params := &ConnectParams{
		AutoSubscribe: true,
	}
	for _, opt := range opts {
		opt(params)
	}

	joinRes, err := r.engine.Join(url, token, params)
	if err != nil {
		return err
	}

	r.lock.Lock()
	r.name = joinRes.Room.Name
	r.sid = joinRes.Room.Sid
	r.metadata = joinRes.Room.Metadata
	r.lock.Unlock()

	r.LocalParticipant.updateInfo(joinRes.Participant)

	for _, pi := range joinRes.OtherParticipants {
		r.addRemoteParticipant(pi, true)
	}

	return nil
}

func (r *Room) Disconnect() {
	_ = r.engine.client.SendLeave()
	r.engine.Close()
}

func (r *Room) GetParticipant(sid string) *RemoteParticipant {
	r.lock.RLock()
	defer r.lock.RUnlock()

	return r.participants[sid]
}

func (r *Room) GetParticipants() []*RemoteParticipant {
	r.lock.RLock()
	defer r.lock.RUnlock()

	var participants []*RemoteParticipant
	for _, rp := range r.participants {
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

func (r *Room) addRemoteParticipant(pi *livekit.ParticipantInfo, updateExisting bool) *RemoteParticipant {
	r.lock.Lock()
	rp, ok := r.participants[pi.Sid]
	if ok {
		if updateExisting {
			rp.updateInfo(pi)
		}
		r.lock.Unlock()
		return rp
	}

	rp = newRemoteParticipant(pi, r.callback, r.engine.client, func(ssrc webrtc.SSRC) {
		pli := []rtcp.Packet{
			&rtcp.PictureLossIndication{SenderSSRC: uint32(ssrc), MediaSSRC: uint32(ssrc)},
		}
		_ = r.engine.subscriber.pc.WriteRTCP(pli)
	})
	r.participants[pi.Sid] = rp
	r.lock.Unlock()

	return rp
}

func (r *Room) handleMediaTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	// ensure we have the participant
	participantID, trackID := unpackStreamID(track.StreamID())
	if trackID == "" {
		trackID = track.ID()
	}

	rp := r.addRemoteParticipant(&livekit.ParticipantInfo{
		Sid: participantID,
	}, false)
	rp.addSubscribedMediaTrack(track, trackID, receiver)
}

func (r *Room) handleDisconnect() {
	r.callback.OnDisconnected()
	r.engine.Close()
}

func (r *Room) handleRestarting() {
	r.callback.OnReconnecting()

	for _, rp := range r.GetParticipants() {
		r.handleParticipantDisconnect(rp)
	}
}

func (r *Room) handleRestarted(joinRes *livekit.JoinResponse) {
	r.lock.Lock()
	r.name = joinRes.Room.Name
	r.sid = joinRes.Room.Sid
	r.metadata = joinRes.Room.Metadata
	r.lock.Unlock()

	r.LocalParticipant.updateInfo(joinRes.Participant)

	r.handleParticipantUpdate(joinRes.OtherParticipants)

	var localPubs []*LocalTrackPublication
	r.LocalParticipant.tracks.Range(func(_, value interface{}) bool {
		track := value.(*LocalTrackPublication)

		if track.Track() != nil {
			localPubs = append(localPubs, track)
		}
		return true
	})

	for _, pub := range localPubs {
		if track := pub.TrackLocal(); track != nil {
			r.LocalParticipant.PublishTrack(track, &TrackPublicationOptions{
				Name: pub.Name(),
			})
		}
	}
	r.callback.OnReconnected()
}

func (r *Room) handleResuming() {
	r.callback.OnReconnecting()
}

func (r *Room) handleResumed() {
	r.callback.OnReconnected()
	r.sendSyncState()
}

func (r *Room) handleDataReceived(userPacket *livekit.UserPacket) {
	if userPacket.ParticipantSid == r.LocalParticipant.sid {
		// if sent by itself, do not handle data
		return
	}
	p := r.GetParticipant(userPacket.ParticipantSid)
	if p == nil {
		return
	}
	p.Callback.OnDataReceived(userPacket.Payload, p)
	r.callback.OnDataReceived(userPacket.Payload, p)
}

func (r *Room) handleParticipantUpdate(participants []*livekit.ParticipantInfo) {
	for _, pi := range participants {
		p := r.GetParticipant(pi.Sid)
		isNew := p == nil

		if pi.State == livekit.ParticipantInfo_DISCONNECTED {
			// remove
			if p != nil {
				r.handleParticipantDisconnect(p)
			}
		} else if isNew {
			p = r.addRemoteParticipant(pi, true)
			go r.callback.OnParticipantConnected(p)
		} else {
			p.updateInfo(pi)
		}
	}
}

func (r *Room) handleParticipantDisconnect(p *RemoteParticipant) {
	r.lock.Lock()
	delete(r.participants, p.SID())
	r.lock.Unlock()

	p.unpublishAllTracks()
	go r.callback.OnParticipantDisconnected(p)
}

func (r *Room) handleActiveSpeakerChange(speakers []*livekit.SpeakerInfo) {
	var activeSpeakers []Participant
	seenSids := make(map[string]bool)
	for _, s := range speakers {
		seenSids[s.Sid] = true
		if s.Sid == r.LocalParticipant.sid {
			r.LocalParticipant.setAudioLevel(s.Level)
			r.LocalParticipant.setIsSpeaking(true)
			activeSpeakers = append(activeSpeakers, r.LocalParticipant)
		} else {
			p := r.GetParticipant(s.Sid)
			if p == nil {
				continue
			}
			p.setAudioLevel(s.Level)
			p.setIsSpeaking(true)
			activeSpeakers = append(activeSpeakers, p)
		}
	}

	if !seenSids[r.LocalParticipant.sid] {
		r.LocalParticipant.setAudioLevel(0)
	}
	for _, rp := range r.GetParticipants() {
		if !seenSids[rp.sid] {
			rp.setAudioLevel(0)
			rp.setIsSpeaking(false)
		}
	}
	r.lock.Lock()
	r.activeSpeakers = activeSpeakers
	r.lock.Unlock()
	go r.callback.OnActiveSpeakersChanged(activeSpeakers)
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
			participant = r.GetParticipant(info.Sid)
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

	activeSpeakers := funk.Values(speakerMap).([]Participant)
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
			p := r.GetParticipant(update.ParticipantSid)
			if p != nil {
				p.setConnectionQualityInfo(update)
			} else {
				logger.Info("could not find participant", "sid", update.ParticipantSid,
					"localParticipant", r.LocalParticipant.SID())
			}
		}
	}
}

func (r *Room) handleRoomUpdate(room *livekit.Room) {
	if r.Metadata() == room.Metadata {
		return
	}
	r.lock.Lock()
	r.metadata = room.Metadata
	r.lock.Unlock()
	go r.callback.OnRoomMetadataChanged(room.Metadata)
}

func (r *Room) handleTrackMuted(msg *livekit.MuteTrackRequest) {
	for _, pub := range r.LocalParticipant.Tracks() {
		if pub.SID() == msg.Sid {
			localPub := pub.(*LocalTrackPublication)
			// TODO: pause sending data because it'll be dropped by SFU
			localPub.SetMuted(msg.Muted)
		}
	}
}

func (r *Room) handleLocalTrackUnpublished(msg *livekit.TrackUnpublishedResponse) {
	err := r.LocalParticipant.UnpublishTrack(msg.TrackSid)
	if err != nil {
		logger.Error(err, "could not unpublish track", "trackID", msg.TrackSid)
	}
}

func (r *Room) sendSyncState() {
	if r.engine.subscriber == nil || r.engine.subscriber.pc.RemoteDescription() == nil {
		return
	}

	previousSdp := r.engine.subscriber.pc.LocalDescription()

	var trackSids []string
	sendUnsub := r.engine.connParams.AutoSubscribe
	for _, rp := range r.GetParticipants() {
		for _, t := range rp.Tracks() {
			if t.IsSubscribed() != sendUnsub {
				trackSids = append(trackSids, t.SID())
			}
		}
	}

	var publishedTracks []*livekit.TrackPublishedResponse
	for _, t := range r.LocalParticipant.Tracks() {
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

	getDCinfo(r.engine.reliableDC, livekit.SignalTarget_PUBLISHER)
	getDCinfo(r.engine.lossyDC, livekit.SignalTarget_PUBLISHER)
	getDCinfo(r.engine.reliableDCSub, livekit.SignalTarget_SUBSCRIBER)
	getDCinfo(r.engine.lossyDCSub, livekit.SignalTarget_SUBSCRIBER)

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

func (r *Room) Simulate(scenario SimulateScenario) {
	switch scenario {
	case SimulateSignalReconnect:
		r.engine.client.Close()
	}
}

func unpackStreamID(packed string) (participantId string, trackId string) {
	parts := strings.Split(packed, "|")
	if len(parts) > 1 {
		return parts[0], packed[len(parts[0])+1:]
	}
	return packed, ""
}
