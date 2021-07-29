package lksdk

import (
	"sync"
	"sync/atomic"

	livekit "github.com/livekit/server-sdk-go/proto"
)

type Participant interface {
	SID() string
	Identity() string
	IsSpeaking() bool
	AudioLevel() float32
	setAudioLevel(level float32)
	setIsSpeaking(speaking bool)
	Tracks() []TrackPublication
	Metadata() string
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

	audioTracks *sync.Map
	videoTracks *sync.Map
	tracks      *sync.Map
}

func newBaseParticipant(roomCallback *RoomCallback) *baseParticipant {
	p := &baseParticipant{
		audioTracks:  &sync.Map{},
		videoTracks:  &sync.Map{},
		tracks:       &sync.Map{},
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

func (p *baseParticipant) Metadata() string {
	return p.metadata
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

func (p *baseParticipant) Tracks() []TrackPublication {
	tracks := make([]TrackPublication, 0)
	p.tracks.Range(func(_, value interface{}) bool {
		track := value.(TrackPublication)
		tracks = append(tracks, track)
		return true
	})
	return tracks
}

func (p *baseParticipant) setAudioLevel(level float32) {
	p.audioLevel.Store(level)
}

func (p *baseParticipant) setIsSpeaking(speaking bool) {
	lastSpeaking := p.IsSpeaking()
	if speaking == lastSpeaking {
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

	sid := publication.SID()
	p.tracks.Store(sid, publication)
	switch publication.Kind() {
	case TrackKindAudio:
		p.audioTracks.Store(sid, publication)
	case TrackKindVideo:
		p.videoTracks.Store(sid, publication)
	}
}

func (p *baseParticipant) getPublication(sid string) TrackPublication {
	p.lock.Lock()
	defer p.lock.Unlock()

	track, ok := p.tracks.Load(sid)
	if !ok {
		return nil
	}
	return track.(TrackPublication)
}
