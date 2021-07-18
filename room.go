package lksdk

import (
	"strings"
	"sync"

	"github.com/livekit/protocol/auth"
	"github.com/pion/webrtc/v3"

	livekit "github.com/livekit/server-sdk-go/proto"
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

type Room struct {
	engine           *RTCEngine
	lock             sync.RWMutex
	SID              string
	Name             string
	LocalParticipant *LocalParticipant
	Participants     map[string]*RemoteParticipant
	ActiveSpeakers   []Participant
	Callback         *RoomCallback
}

func ConnectToRoom(url string, info ConnectInfo) (*Room, error) {
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

	return ConnectToRoomWithToken(url, token)
}

func ConnectToRoomWithToken(url, token string) (*Room, error) {
	engine := NewRTCEngine()
	r := &Room{
		engine:       engine,
		Participants: make(map[string]*RemoteParticipant),
		Callback:     NewRoomCallback(),
	}
	r.LocalParticipant = newLocalParticipant(engine, r.Callback)

	// callbacks from engine
	engine.OnMediaTrack = r.handleMediaTrack
	engine.OnDisconnected = r.handleDisconnect
	engine.OnParticipantUpdate = r.handleParticipantUpdate
	engine.OnActiveSpeakersChanged = r.handleActiveSpeakerChange

	joinRes, err := engine.Join(url, token)
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
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.Participants[sid]
}

func (r *Room) GetParticipants() []*RemoteParticipant {
	var participants []*RemoteParticipant
	r.lock.RLock()
	defer r.lock.RUnlock()
	for _, p := range r.Participants {
		participants = append(participants, p)
	}
	return participants
}

func (r *Room) addRemoteParticipant(pi *livekit.ParticipantInfo) *RemoteParticipant {
	r.lock.Lock()
	defer r.lock.Unlock()

	p := r.Participants[pi.Sid]

	if p == nil {
		p = newRemoteParticipant(pi, r.Callback)
		r.Participants[pi.Sid] = p
	}
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
	r.lock.Lock()
	defer r.lock.Unlock()
	delete(r.Participants, p.SID())
	p.unpublishAllTracks()
	r.Callback.OnParticipantDisconnected(p)
}

func (r *Room) handleActiveSpeakerChange(speakers []*livekit.SpeakerInfo) {
	var activeSpeakers []Participant
	seenSids := make(map[string]bool)
	for _, s := range speakers {
		seenSids[s.Sid] = true
		var p Participant

		if s.Sid == r.LocalParticipant.sid {
			p = r.LocalParticipant
		} else {
			p = r.GetParticipant(s.Sid)
		}

		if p != nil {
			p.setAudioLevel(s.Level)
			p.setIsSpeaking(true)
			activeSpeakers = append(activeSpeakers, p)
		}
	}

	if !seenSids[r.LocalParticipant.sid] {
		r.LocalParticipant.setAudioLevel(0)
	}
	r.lock.RLock()
	for _, p := range r.Participants {
		if !seenSids[p.sid] {
			p.setAudioLevel(0)
			p.setIsSpeaking(false)
		}
	}
	r.ActiveSpeakers = activeSpeakers
	r.lock.RUnlock()

	r.Callback.OnActiveSpeakersChanged(activeSpeakers)
}

func unpackStreamID(packed string) (participantId string, trackId string) {
	parts := strings.Split(packed, "|")
	if len(parts) > 1 {
		return parts[0], packed[len(parts[0])+1:]
	}
	return packed, ""
}
