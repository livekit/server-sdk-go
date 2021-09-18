package lksdk

import (
	"sort"
	"strings"
	"sync"

	"github.com/livekit/protocol/auth"
	livekit "github.com/livekit/protocol/proto"
	"github.com/pion/webrtc/v3"
	"github.com/thoas/go-funk"
)

type TrackPubCallback func(track Track, pub TrackPublication, participant *RemoteParticipant)
type PubCallback func(pub TrackPublication, participant *RemoteParticipant)

type ConnectInfo struct {
	APIKey              string
	APISecret           string
	RoomName            string
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

type Room struct {
	engine           *RTCEngine
	SID              string
	Name             string
	LocalParticipant *LocalParticipant
	participants     *sync.Map
	ActiveSpeakers   []Participant
	Callback         *RoomCallback
}

func ConnectToRoom(url string, info ConnectInfo, opts ...ConnectOption) (*Room, error) {
	// generate token
	at := auth.NewAccessToken(info.APIKey, info.APISecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     info.RoomName,
	}
	at.AddGrant(grant).
		SetIdentity(info.ParticipantIdentity).
		SetMetadata(info.ParticipantMetadata)

	token, err := at.ToJWT()
	if err != nil {
		return nil, err
	}

	return ConnectToRoomWithToken(url, token, opts...)
}

func ConnectToRoomWithToken(url, token string, opts ...ConnectOption) (*Room, error) {
	params := &ConnectParams{
		AutoSubscribe: true,
	}
	for _, opt := range opts {
		opt(params)
	}

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

	joinRes, err := engine.Join(url, token, params)
	if err != nil {
		return nil, err
	}
	r.LocalParticipant.updateInfo(joinRes.Participant)

	for _, pi := range joinRes.OtherParticipants {
		r.addRemoteParticipant(pi)
	}

	return r, nil
}

func (r *Room) Disconnect() {
	_ = r.engine.client.SendLeave()
	r.engine.Close()
}

func (r *Room) GetParticipant(sid string) *RemoteParticipant {
	partRaw, ok := r.participants.Load(sid)
	if !ok {
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

func (r *Room) addRemoteParticipant(pi *livekit.ParticipantInfo) *RemoteParticipant {

	pRaw, ok := r.participants.Load(pi.Sid)
	if ok {
		return pRaw.(*RemoteParticipant)
	}
	p := newRemoteParticipant(pi, r.Callback)
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
			r.Callback.OnParticipantConnected(p)
		} else {
			p.updateInfo(pi)
		}
	}
}

func (r *Room) handleParticipantDisconnect(p *RemoteParticipant) {
	r.participants.Delete(p.SID())
	p.unpublishAllTracks()
	r.Callback.OnParticipantDisconnected(p)
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
	r.ActiveSpeakers = activeSpeakers
	r.Callback.OnActiveSpeakersChanged(activeSpeakers)
}

func (r *Room) handleSpeakersChange(speakerUpdates []*livekit.SpeakerInfo) {
	speakerMap := make(map[string]Participant)
	for _, p := range r.ActiveSpeakers {
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
	r.ActiveSpeakers = activeSpeakers
	r.Callback.OnActiveSpeakersChanged(activeSpeakers)
}

func unpackStreamID(packed string) (participantId string, trackId string) {
	parts := strings.Split(packed, "|")
	if len(parts) > 1 {
		return parts[0], packed[len(parts[0])+1:]
	}
	return packed, ""
}
