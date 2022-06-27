package lksdk

import (
	"sync"

	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"
)

type Participant interface {
	SID() string
	Identity() string
	Name() string
	IsSpeaking() bool
	AudioLevel() float32
	Tracks() []TrackPublication
	IsCameraEnabled() bool
	IsMicrophoneEnabled() bool
	IsScreenShareEnabled() bool
	Metadata() string
	GetTrack(source livekit.TrackSource) TrackPublication

	setAudioLevel(level float32)
	setIsSpeaking(speaking bool)
	setConnectionQualityInfo(info *livekit.ConnectionQualityInfo)
}

type baseParticipant struct {
	sid               string
	identity          string
	name              string
	audioLevel        atomic.Float64
	metadata          string
	isSpeaking        atomic.Bool
	info              *livekit.ParticipantInfo
	connectionQuality *livekit.ConnectionQualityInfo
	lock              sync.RWMutex

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
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.sid
}

func (p *baseParticipant) Identity() string {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.identity
}

func (p *baseParticipant) Name() string {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.name
}

func (p *baseParticipant) Metadata() string {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.metadata
}

func (p *baseParticipant) IsSpeaking() bool {
	return p.isSpeaking.Load()
}

func (p *baseParticipant) AudioLevel() float32 {
	return float32(p.audioLevel.Load())
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

func (p *baseParticipant) GetTrack(source livekit.TrackSource) TrackPublication {
	var pub TrackPublication
	p.tracks.Range(func(_, value interface{}) bool {
		trackPub := value.(TrackPublication)
		if trackPub.Source() == source {
			pub = trackPub
			return false
		}
		return true
	})
	return pub
}

func (p *baseParticipant) IsCameraEnabled() bool {
	pub := p.GetTrack(livekit.TrackSource_CAMERA)
	return pub != nil && !pub.IsMuted()
}

func (p *baseParticipant) IsMicrophoneEnabled() bool {
	pub := p.GetTrack(livekit.TrackSource_MICROPHONE)
	return pub != nil && !pub.IsMuted()
}

func (p *baseParticipant) IsScreenShareEnabled() bool {
	pub := p.GetTrack(livekit.TrackSource_SCREEN_SHARE)
	return pub != nil && !pub.IsMuted()
}

func (p *baseParticipant) setAudioLevel(level float32) {
	p.audioLevel.Store(float64(level))
}

func (p *baseParticipant) setIsSpeaking(speaking bool) {
	if p.isSpeaking.Swap(speaking) == speaking {
		return
	}
	p.Callback.OnIsSpeakingChanged(p)
	p.roomCallback.OnIsSpeakingChanged(p)
}

func (p *baseParticipant) setConnectionQualityInfo(info *livekit.ConnectionQualityInfo) {
	p.lock.Lock()
	p.connectionQuality = info
	p.lock.Unlock()
	p.Callback.OnConnectionQualityChanged(info, p)
	p.roomCallback.OnConnectionQualityChanged(info, p)
}

func (p *baseParticipant) updateInfo(pi *livekit.ParticipantInfo, participant Participant) {
	p.lock.Lock()
	p.info = pi
	p.identity = pi.Identity
	p.sid = pi.Sid
	p.name = pi.Name
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
