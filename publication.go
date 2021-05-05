package lksdk

import (
	livekit "github.com/livekit/livekit-sdk-go/proto"
	"github.com/pion/webrtc/v3"
)

type TrackKind string

const (
	TrackKindVideo TrackKind = "video"
	TrackKindAudio TrackKind = "audio"
)

type TrackPublication interface {
	Name() string
	SID() string
	Kind() TrackKind
	IsMuted() bool
	IsSubscribed() bool
	Track() Track
	updateInfo(info *livekit.TrackInfo)
}

type trackPublicationBase struct {
	kind    TrackKind
	track   Track
	sid     string
	name    string
	isMuted bool

	// only for Remote publications
	receiver *webrtc.RTPReceiver

	client *SignalClient
}

func (p *trackPublicationBase) Name() string {
	return p.name
}

func (p *trackPublicationBase) SID() string {
	return p.sid
}

func (p *trackPublicationBase) Kind() TrackKind {
	return p.kind
}

func (p *trackPublicationBase) Track() Track {
	return p.track
}

func (p *trackPublicationBase) IsMuted() bool {
	return p.isMuted
}

func (p *trackPublicationBase) IsSubscribed() bool {
	return p.track != nil
}

func (p *trackPublicationBase) updateInfo(info *livekit.TrackInfo) {
	p.name = info.Name
	p.sid = info.Sid
	p.isMuted = info.Muted
	if info.Type == livekit.TrackType_AUDIO {
		p.kind = TrackKindAudio
	} else if info.Type == livekit.TrackType_VIDEO {
		p.kind = TrackKindVideo
	}
}

type RemoteTrackPublication struct {
	trackPublicationBase
}

func (p *RemoteTrackPublication) TrackRemote() *webrtc.TrackRemote {
	if t, ok := p.track.(*webrtc.TrackRemote); ok {
		return t
	}
	return nil
}

func (p *RemoteTrackPublication) Receiver() *webrtc.RTPReceiver {
	return p.receiver
}

type LocalTrackPublication struct {
	trackPublicationBase
}

func (p *LocalTrackPublication) TrackLocal() webrtc.TrackLocal {
	if t, ok := p.track.(webrtc.TrackLocal); ok {
		return t
	}
	return nil
}
