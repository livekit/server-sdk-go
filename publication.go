package lksdk

import (
	"sync"
	"sync/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/pion/rtcp"
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
	// OnRTCP sets a callback to handle RTCP packets during publishing or receiving
	OnRTCP(cb func(rtcp.Packet))
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

	lock   sync.Mutex
	info   atomic.Value
	client *SignalClient
	onRTCP func(rtcp.Packet)
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

func (p *trackPublicationBase) OnRTCP(cb func(rtcp.Packet)) {
	p.lock.Lock()
	p.onRTCP = cb
	p.lock.Unlock()
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
	p.lock.Lock()
	defer p.lock.Unlock()
	if t, ok := p.track.(*webrtc.TrackRemote); ok {
		return t
	}
	return nil
}

func (p *RemoteTrackPublication) Receiver() *webrtc.RTPReceiver {
	p.lock.Lock()
	defer p.lock.Unlock()
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

func (p *RemoteTrackPublication) setReceiverAndTrack(r *webrtc.RTPReceiver, t *webrtc.TrackRemote) {
	p.lock.Lock()
	p.receiver = r
	p.track = t
	p.lock.Unlock()
	if r != nil {
		go p.rtcpWorker()
	}
}

func (p *RemoteTrackPublication) rtcpWorker() {
	receiver := p.Receiver()
	if receiver == nil {
		return
	}
	// read incoming rtcp packets so interceptors can handle NACKs
	for {
		packets, _, err := receiver.ReadRTCP()
		if err != nil {
			// pipe closed
			return
		}

		p.lock.Lock()
		// rtcpCB could have changed along the way
		rtcpCB := p.onRTCP
		p.lock.Unlock()
		if rtcpCB != nil {
			for _, packet := range packets {
				rtcpCB(packet)
			}
		}
	}
}

type LocalTrackPublication struct {
	trackPublicationBase
	sender *webrtc.RTPSender
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

func (p *LocalTrackPublication) setSender(sender *webrtc.RTPSender) {
	p.lock.Lock()
	p.sender = sender
	p.lock.Unlock()

	if sender != nil {
		go p.rtcpWorker()
	}
}

func (p *LocalTrackPublication) rtcpWorker() {
	p.lock.Lock()
	sender := p.sender
	p.lock.Unlock()
	// read incoming rtcp packets, interceptors require this
	for {
		packets, _, rtcpErr := sender.ReadRTCP()
		if rtcpErr != nil {
			// pipe closed
			return
		}

		p.lock.Lock()
		// rtcpCB could have changed along the way
		rtcpCB := p.onRTCP
		p.lock.Unlock()
		if rtcpCB != nil {
			for _, packet := range packets {
				rtcpCB(packet)
			}
		}
	}
}

type TrackPublicationOptions struct {
	Name   string
	Source livekit.TrackSource
}
