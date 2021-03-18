package lksdk

import (
	"sync"
	"sync/atomic"

	livekit "github.com/livekit/livekit-sdk-go/proto"
)

type Participant interface {
	SID() string
	Identity() string
	AudioLevel() float32
	setAudioLevel(level float32)
}

type baseParticipant struct {
	sid        string
	identity   string
	audioLevel atomic.Value
	lock       sync.Mutex

	audioTracks map[string]*TrackPublication
	videoTracks map[string]*TrackPublication
	dataTracks  map[string]*TrackPublication
	tracks      map[string]*TrackPublication
}

func newBaseParticipant() *baseParticipant {
	p := &baseParticipant{
		audioTracks: newTrackMap(),
		videoTracks: newTrackMap(),
		dataTracks:  newTrackMap(),
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

func (p *baseParticipant) AudioLevel() float32 {
	return p.audioLevel.Load().(float32)
}

func (p *baseParticipant) setAudioLevel(level float32) {
	p.audioLevel.Store(level)
}

func (p *baseParticipant) updateMetadata(pi *livekit.ParticipantInfo) {
	p.lock.Lock()
	p.identity = pi.Identity
	p.sid = pi.Sid
	p.lock.Unlock()
}

func (p *baseParticipant) addPublication(publication *TrackPublication) {
	p.lock.Lock()
	defer p.lock.Unlock()

	sid := publication.SID
	p.tracks[sid] = publication
	switch publication.Kind {
	case TrackKindAudio:
		p.audioTracks[sid] = publication
	case TrackKindVideo:
		p.videoTracks[sid] = publication
	case TrackKindData:
		p.dataTracks[sid] = publication
	}
}

func newTrackMap() map[string]*TrackPublication {
	return make(map[string]*TrackPublication)
}
