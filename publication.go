package lksdk

import (
	"sync/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/pion/webrtc/v3"
)

type TrackPublication interface {
	Name() string
	SID() string
	Source() livekit.TrackSource
	Kind() TrackKind
	MimeType() string
	IsMuted() bool
	IsSubscribed() bool
	// Track is either a webrtc.TrackLocal or webrtc.TrackRemote
	Track() Track
	updateInfo(info *livekit.TrackInfo)
}

type trackPublicationBase struct {
	kind    TrackKind
	track   Track
	sid     string
	name    string
	isMuted uint32

	info   atomic.Value
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

func (p *trackPublicationBase) MimeType() string {
	if info, ok := p.info.Load().(*livekit.TrackInfo); ok {
		return info.MimeType
	}
	return ""
}

func (p *trackPublicationBase) Source() livekit.TrackSource {
	if info, ok := p.info.Load().(*livekit.TrackInfo); ok {
		return info.Source
	}
	return livekit.TrackSource_UNKNOWN
}

func (p *trackPublicationBase) IsMuted() bool {
	return atomic.LoadUint32(&p.isMuted) == 1
}

func (p *trackPublicationBase) IsSubscribed() bool {
	return p.track != nil
}

func (p *trackPublicationBase) updateInfo(info *livekit.TrackInfo) {
	p.name = info.Name
	p.sid = info.Sid
	val := uint32(0)
	if info.Muted {
		val = 1
	}
	atomic.StoreUint32(&p.isMuted, val)
	if info.Type == livekit.TrackType_AUDIO {
		p.kind = TrackKindAudio
	} else if info.Type == livekit.TrackType_VIDEO {
		p.kind = TrackKindVideo
	}
	p.info.Store(info)
}

type RemoteTrackPublication struct {
	trackPublicationBase
	receiver *webrtc.RTPReceiver
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

func (p *RemoteTrackPublication) SetSubscribed(subscribed bool) error {
	return p.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Subscription{
			Subscription: &livekit.UpdateSubscription{
				Subscribe: subscribed,
				ParticipantTracks: []*livekit.ParticipantTracks{
					{
						// sending an empty participant id since TrackPublication doesn't keep it
						// this is filled in by the participant that receives this message
						ParticipantSid: "",
						TrackSids:      []string{p.sid},
					},
				},
			},
		},
	})
}

type LocalTrackPublication struct {
	trackPublicationBase
	transceiver *webrtc.RTPTransceiver
}

func (p *LocalTrackPublication) TrackLocal() webrtc.TrackLocal {
	if t, ok := p.track.(webrtc.TrackLocal); ok {
		return t
	}
	return nil
}

func (p *LocalTrackPublication) SetMuted(muted bool) {
	if p.IsMuted() == muted {
		return
	}
	val := uint32(0)
	if muted {
		val = 1
	}
	atomic.StoreUint32(&p.isMuted, val)

	_ = p.client.SendMuteTrack(p.sid, muted)
}

type TrackPublicationOptions struct {
	Name string
}
