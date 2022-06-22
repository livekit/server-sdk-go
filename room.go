package lksdk

import (
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
	SID              string
	Name             string
	LocalParticipant *LocalParticipant
	Callback         *RoomCallback

	participants   *sync.Map
	metadata       string
	activeSpeakers []Participant

	lock sync.RWMutex
}

// CreateRoom can be used to update callbacks before calling Join
func CreateRoom() *Room {
	engine := NewRTCEngine()
	r := &Room{
		engine:       engine,
		participants: &sync.Map{},
		Callback:     NewRoomCallback(),
	}
	r.LocalParticipant = newLocalParticipant(engine, r.Callback)

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
func ConnectToRoom(url string, info ConnectInfo, opts ...ConnectOption) (*Room, error) {
	room := CreateRoom()
	err := room.Join(url, info, opts...)
	if err != nil {
		return nil, err
	}
	return room, nil
}

// ConnectToRoomWithToken creates and joins the room
func ConnectToRoomWithToken(url, token string, opts ...ConnectOption) (*Room, error) {
	room := CreateRoom()
	err := room.JoinWithToken(url, token, opts...)
	if err != nil {
		return nil, err
	}
	return room, nil
}

// Join should only be used with CreateRoom
func (r *Room) Join(url string, info ConnectInfo, opts ...ConnectOption) error {
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

	r.Name = joinRes.Room.Name
	r.SID = joinRes.Room.Sid
	r.metadata = joinRes.Room.Metadata

	r.LocalParticipant.updateInfo(joinRes.Participant)

	for _, pi := range joinRes.OtherParticipants {
		r.addRemoteParticipant(pi)
	}

	return nil
}

func (r *Room) Disconnect() {
	_ = r.engine.client.SendLeave()
	r.engine.Close()
}

func (r *Room) GetParticipant(sid string) *RemoteParticipant {
	partRaw, ok := r.participants.Load(sid)
	if !ok || partRaw == nil {
		return nil
	}
	return partRaw.(*RemoteParticipant)
}

func (r *Room) GetParticipants() []*RemoteParticipant {
	var participants []*RemoteParticipant
	r.participants.Range(func(_, value interface{}) bool {
		participants = append(participants, value.(*RemoteParticipant))
		return true
	})
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

func (r *Room) addRemoteParticipant(pi *livekit.ParticipantInfo) *RemoteParticipant {
	pRaw, ok := r.participants.Load(pi.Sid)
	if ok {
		return pRaw.(*RemoteParticipant)
	}
	p := newRemoteParticipant(pi, r.Callback, r.engine.client, func(ssrc webrtc.SSRC) {
		pli := []rtcp.Packet{
			&rtcp.PictureLossIndication{SenderSSRC: uint32(ssrc), MediaSSRC: uint32(ssrc)},
		}
		_ = r.engine.subscriber.pc.WriteRTCP(pli)
	})
	r.participants.Store(pi.Sid, p)
	return p
}

func (r *Room) handleMediaTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	// ensure we have the participant
	participantID, trackID := unpackStreamID(track.StreamID())
	if trackID == "" {
		trackID = track.ID()
	}

	p := r.GetParticipant(participantID)
	if p == nil {
		p = r.addRemoteParticipant(&livekit.ParticipantInfo{
			Sid: participantID,
		})
	}
	p.addSubscribedMediaTrack(track, trackID, receiver)
}

func (r *Room) handleDisconnect() {
	r.Callback.OnDisconnected()
	r.engine.Close()
}

func (r *Room) handleRestarting() {
	r.Callback.OnReconnecting()
	r.participants.Range(func(_, value interface{}) bool {
		r.handleParticipantDisconnect(value.(*RemoteParticipant))
		return true
	})
}

func (r *Room) handleRestarted(joinRes *livekit.JoinResponse) {
	r.lock.Lock()
	r.Name = joinRes.Room.Name
	r.SID = joinRes.Room.Sid
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
				Name: pub.name,
			})
		}
	}
	r.Callback.OnReconnected()
}

func (r *Room) handleResuming() {
	r.Callback.OnReconnecting()
}

func (r *Room) handleResumed() {
	r.Callback.OnReconnected()
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
	r.Callback.OnDataReceived(userPacket.Payload, p)
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
			p = r.addRemoteParticipant(pi)
			go r.Callback.OnParticipantConnected(p)
		} else {
			p.updateInfo(pi)
		}
	}
}

func (r *Room) handleParticipantDisconnect(p *RemoteParticipant) {
	r.participants.Delete(p.SID())
	p.unpublishAllTracks()
	go r.Callback.OnParticipantDisconnected(p)
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
	r.participants.Range(func(_, value interface{}) bool {
		p := value.(*RemoteParticipant)
		if !seenSids[p.sid] {
			p.setAudioLevel(0)
			p.setIsSpeaking(false)
		}
		return true
	})
	r.lock.Lock()
	r.activeSpeakers = activeSpeakers
	r.lock.Unlock()
	go r.Callback.OnActiveSpeakersChanged(activeSpeakers)
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
			if obj, ok := r.participants.Load(info.Sid); ok {
				if p, ok := obj.(Participant); ok {
					participant = p
				}
			}
		}
		if participant == nil {
			break
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
	go r.Callback.OnActiveSpeakersChanged(activeSpeakers)
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
	go r.Callback.OnRoomMetadataChanged(room.Metadata)
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
	r.participants.Range(func(_, val interface{}) bool {
		p := val.(*RemoteParticipant)
		for _, t := range p.Tracks() {
			if t.IsSubscribed() != sendUnsub {
				trackSids = append(trackSids, t.SID())
			}
		}
		return true
	})

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
