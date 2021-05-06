package lksdk

import (
	"sync"
	"sync/atomic"

	livekit "github.com/livekit/livekit-sdk-go/proto"
)

type Participant interface {
	SID() string
	Identity() string
	IsSpeaking() bool
	AudioLevel() float32
	setAudioLevel(level float32)
	setIsSpeaking(speaking bool)
}

type baseParticipant struct {
	sid        string
	identity   string
	audioLevel atomic.Value
	metadata   string
	isSpeaking atomic.Value
	info       *livekit.ParticipantInfo
	lock       sync.Mutex

	Callback     *ParticipantCallback
	roomCallback *RoomCallback

	audioTracks map[string]TrackPublication
	videoTracks map[string]TrackPublication
	tracks      map[string]TrackPublication
}

func newBaseParticipant(roomCallback *RoomCallback) *baseParticipant {
	p := &baseParticipant{
		audioTracks:  newTrackMap(),
		videoTracks:  newTrackMap(),
		tracks:       newTrackMap(),
		roomCallback: roomCallback,
		Callback:     NewParticipantCallback(),
	}
	// need to initialize
	p.setAudioLevel(0)
	p.setIsSpeaking(false)
	return p
}

func (p *baseParticipant) SID() string {
	return p.sid
}

func (p *baseParticipant) Identity() string {
	return p.identity
}

func (p *baseParticipant) IsSpeaking() bool {
	val := p.isSpeaking.Load()
	if speaking, ok := val.(bool); ok {
		return speaking
	}
	return false
}

func (p *baseParticipant) AudioLevel() float32 {
	return p.audioLevel.Load().(float32)
}

func (p *baseParticipant) setAudioLevel(level float32) {
	p.audioLevel.Store(level)
}

func (p *baseParticipant) setIsSpeaking(speaking bool) {
	lastSpeaking := p.IsSpeaking()
	if speaking != lastSpeaking {
		return
	}
	p.isSpeaking.Store(speaking)
	p.Callback.OnIsSpeakingChanged(p)
	p.roomCallback.OnIsSpeakingChanged(p)
}

func (p *baseParticipant) updateInfo(pi *livekit.ParticipantInfo, participant Participant) {
	p.lock.Lock()
	p.info = pi
	p.identity = pi.Identity
	p.sid = pi.Sid
	oldMetadata := p.metadata
	p.metadata = pi.Metadata
	p.lock.Unlock()

	if oldMetadata != p.metadata {
		p.Callback.OnMetadataChanged(oldMetadata, participant)
		p.roomCallback.OnMetadataChanged(oldMetadata, participant)
	}
}

func (p *baseParticipant) addPublication(publication TrackPublication) {
	p.lock.Lock()
	defer p.lock.Unlock()

	sid := publication.SID()
	p.tracks[sid] = publication
	switch publication.Kind() {
	case TrackKindAudio:
		p.audioTracks[sid] = publication
	case TrackKindVideo:
		p.videoTracks[sid] = publication
	}
}

func (p *baseParticipant) getPublication(sid string) TrackPublication {
	p.lock.Lock()
	defer p.lock.Unlock()
	return p.tracks[sid]
}

func newTrackMap() map[string]TrackPublication {
	return make(map[string]TrackPublication)
}
