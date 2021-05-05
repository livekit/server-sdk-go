package lksdk

import (
	"strings"
	"sync"

	"github.com/livekit/protocol/auth"
	"github.com/pion/webrtc/v3"

	livekit "github.com/livekit/livekit-sdk-go/proto"
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
	lock             sync.Mutex
	SID              string
	Name             string
	LocalParticipant *LocalParticipant
	Participants     map[string]*RemoteParticipant
	ActiveSpeakers   []Participant

	// callbacks
	OnDisconnected            func()
	OnParticipantConnected    func(*RemoteParticipant)
	OnParticipantDisconnected func(*RemoteParticipant)
	OnTrackPublished          PubCallback
	OnTrackUnpublished        PubCallback
	OnTrackSubscribed         TrackPubCallback
	OnTrackUnsubscribed       TrackPubCallback
	OnTrackMuted              TrackPubCallback
	OnTrackUnmuted            TrackPubCallback
	OnActiveSpeakersChanged   func([]Participant)
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
	joinRes, err := engine.Join(url, token)
	if err != nil {
		return nil, err
	}

	r := &Room{
		engine:           engine,
		LocalParticipant: newLocalParticipant(engine),
		Participants:     make(map[string]*RemoteParticipant),
	}
	r.LocalParticipant.updateInfo(joinRes.Participant)

	for _, pi := range joinRes.OtherParticipants {
		r.addRemoteParticipant(pi)
	}

	// callbacks from engine
	engine.OnMediaTrack = r.handleMediaTrack
	engine.OnDisconnected = r.handleDisconnect
	engine.OnParticipantUpdate = r.handleParticipantUpdate
	engine.OnActiveSpeakersChanged = r.handleActiveSpeakerChange

	return r, nil
}

func (r *Room) Disconnect() {
	r.engine.Close()
}

func (r *Room) GetParticipant(sid string) *RemoteParticipant {
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.Participants[sid]
}

func (r *Room) addRemoteParticipant(pi *livekit.ParticipantInfo) *RemoteParticipant {
	r.lock.Lock()
	defer r.lock.Unlock()

	p := r.Participants[pi.Sid]

	if p == nil {
		p = newRemoteParticipant(pi)
		r.Participants[pi.Sid] = p

		// event listeners
		p.onTrackPublished = func(publication TrackPublication, rp *RemoteParticipant) {
			if r.OnTrackPublished != nil {
				r.OnTrackPublished(publication, p)
			}
		}
		p.onTrackSubscribed = func(track *webrtc.TrackRemote, pub TrackPublication, rp *RemoteParticipant) {
			if r.OnTrackSubscribed != nil {
				r.OnTrackSubscribed(track, pub, p)
			}
		}
		p.onTrackUnpublished = func(pub TrackPublication, rp *RemoteParticipant) {
			if r.OnTrackUnpublished != nil {
				r.OnTrackUnpublished(pub, p)
			}
		}
		p.onTrackUnsubscribed = func(track *webrtc.TrackRemote, pub TrackPublication, rp *RemoteParticipant) {
			if r.OnTrackUnsubscribed != nil {
				r.OnTrackUnsubscribed(track, pub, p)
			}
		}
		p.onTrackMuted = func(track Track, pub TrackPublication) {
			if r.OnTrackMuted != nil {
				r.OnTrackMuted(track, pub, p)
			}
		}
		p.onTrackUnmuted = func(track Track, pub TrackPublication) {
			if r.OnTrackUnmuted != nil {
				r.OnTrackUnmuted(track, pub, p)
			}
		}
		p.onTrackMessage = func(msg webrtc.DataChannelMessage, rp *RemoteParticipant) {
			// TODO
		}
		p.onTrackSubscriptionFailed = func(sid string, rp *RemoteParticipant) {
			// TODO
		}
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
	if r.OnDisconnected != nil {
		r.OnDisconnected()
	}
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
			if r.OnParticipantConnected != nil {
				r.OnParticipantConnected(p)
			}
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
	if r.OnParticipantConnected != nil {
		r.OnParticipantConnected(p)
	}
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
			activeSpeakers = append(activeSpeakers, p)
		}
	}

	if !seenSids[r.LocalParticipant.sid] {
		r.LocalParticipant.setAudioLevel(0)
	}
	r.lock.Lock()
	for _, p := range r.Participants {
		if !seenSids[p.sid] {
			p.setAudioLevel(0)
		}
	}
	r.ActiveSpeakers = activeSpeakers
	r.lock.Unlock()

	if r.OnActiveSpeakersChanged != nil {
		r.OnActiveSpeakersChanged(activeSpeakers)
	}
}

func unpackStreamID(packed string) (participantId string, trackId string) {
	parts := strings.Split(packed, "|")
	if len(parts) > 1 {
		return parts[0], packed[len(parts[0])+1:]
	}
	return packed, ""
}
