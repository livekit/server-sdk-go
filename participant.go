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
}

type baseParticipant struct {
	sid        string
	identity   string
	audioLevel atomic.Value
	metadata   string
	isSpeaking bool
	info       *livekit.ParticipantInfo
	lock       sync.Mutex

	onTrackMuted      func(track Track, pub TrackPublication)
	onTrackUnmuted    func(track Track, pub TrackPublication)
	onMetadataChanged func(oldMetadata string, p Participant)

	audioTracks map[string]TrackPublication
	videoTracks map[string]TrackPublication
	tracks      map[string]TrackPublication
}

func newBaseParticipant() *baseParticipant {
	p := &baseParticipant{
		audioTracks: newTrackMap(),
		videoTracks: newTrackMap(),
		tracks:      newTrackMap(),
	}
	// need to initialize
	p.setAudioLevel(0)
	return p
}

func (p *baseParticipant) SID() string {
	return p.sid
}

func (p *baseParticipant) Identity() string {
	return p.identity
}

func (p *baseParticipant) IsSpeaking() bool {
	return p.isSpeaking
}

func (p *baseParticipant) AudioLevel() float32 {
	return p.audioLevel.Load().(float32)
}

func (p *baseParticipant) setAudioLevel(level float32) {
	p.audioLevel.Store(level)
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
		if p.onMetadataChanged != nil {
			p.onMetadataChanged(oldMetadata, participant)
		}
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
