package lksdk

import (
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
)

type TrackPublication interface {
	Name() string
	SID() string
	Source() livekit.TrackSource
	Kind() TrackKind
	MimeType() string
	IsMuted() bool
	IsSubscribed() bool
	TrackInfo() *livekit.TrackInfo
	// Track is either a webrtc.TrackLocal or webrtc.TrackRemote
	Track() Track
	updateInfo(info *livekit.TrackInfo)
}

type trackPublicationBase struct {
	kind    atomic.String
	track   Track
	sid     atomic.String
	name    atomic.String
	isMuted atomic.Bool

	lock   sync.RWMutex
	info   atomic.Value
	client *SignalClient
}

func (p *trackPublicationBase) Name() string {
	return p.name.Load()
}

func (p *trackPublicationBase) SID() string {
	return p.sid.Load()
}

func (p *trackPublicationBase) Kind() TrackKind {
	return TrackKind(p.kind.Load())
}

func (p *trackPublicationBase) Track() Track {
	p.lock.RLock()
	defer p.lock.RUnlock()
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
	return p.isMuted.Load()
}

func (p *trackPublicationBase) IsSubscribed() bool {
	return p.track != nil
}

func (p *trackPublicationBase) updateInfo(info *livekit.TrackInfo) {
	p.name.Store(info.Name)
	p.sid.Store(info.Sid)
	p.isMuted.Store(info.Muted)
	if info.Type == livekit.TrackType_AUDIO {
		p.kind.Store(string(TrackKindAudio))
	} else if info.Type == livekit.TrackType_VIDEO {
		p.kind.Store(string(TrackKindVideo))
	}
	p.info.Store(info)
}

func (p *trackPublicationBase) TrackInfo() *livekit.TrackInfo {
	if info := p.info.Load(); info != nil {
		return proto.Clone(info.(*livekit.TrackInfo)).(*livekit.TrackInfo)
	}
	return nil
}

type RemoteTrackPublication struct {
	trackPublicationBase
	participantID string
	receiver      *webrtc.RTPReceiver
	onRTCP        func(rtcp.Packet)

	disabled bool

	// preferred video dimensions to subscribe
	videoWidth  uint32
	videoHeight uint32
}

func (p *RemoteTrackPublication) TrackRemote() *webrtc.TrackRemote {
	p.lock.RLock()
	defer p.lock.RUnlock()
	if t, ok := p.track.(*webrtc.TrackRemote); ok {
		return t
	}
	return nil
}

func (p *RemoteTrackPublication) Receiver() *webrtc.RTPReceiver {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.receiver
}

func (p *RemoteTrackPublication) SetSubscribed(subscribed bool) error {
	return p.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Subscription{
			Subscription: &livekit.UpdateSubscription{
				Subscribe: subscribed,
				ParticipantTracks: []*livekit.ParticipantTracks{
					{
						ParticipantSid: p.participantID,
						TrackSids:      []string{p.sid.Load()},
					},
				},
			},
		},
	})
}

func (p *RemoteTrackPublication) IsEnabled() bool {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return !p.disabled
}

func (p *RemoteTrackPublication) SetEnabled(enabled bool) {
	p.lock.Lock()
	p.disabled = !enabled
	p.lock.Unlock()

	p.updateSettings()
}

func (p *RemoteTrackPublication) SetVideoDimensions(width uint32, height uint32) {
	p.lock.Lock()
	p.videoWidth = width
	p.videoHeight = height
	p.lock.Unlock()

	p.updateSettings()
}

func (p *RemoteTrackPublication) OnRTCP(cb func(rtcp.Packet)) {
	p.lock.Lock()
	p.onRTCP = cb
	p.lock.Unlock()
}

func (p *RemoteTrackPublication) updateSettings() {
	p.lock.Lock()
	settings := &livekit.UpdateTrackSettings{
		TrackSids: []string{p.SID()},
		Disabled:  p.disabled,
		Quality:   livekit.VideoQuality_HIGH,
		Width:     p.videoWidth,
		Height:    p.videoHeight,
	}
	p.lock.Unlock()

	if err := p.client.SendUpdateTrackSettings(settings); err != nil {
		logger.Error(err, "could not send track settings", "trackID", p.SID())
	}
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

		p.lock.RLock()
		// rtcpCB could have changed along the way
		rtcpCB := p.onRTCP
		p.lock.RUnlock()
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
	// set for simulcasted tracks
	simulcastTracks map[livekit.VideoQuality]*LocalSampleTrack
}

func NewLocalTrackPublication(kind TrackKind, track Track, name string, client *SignalClient) *LocalTrackPublication {
	pub := &LocalTrackPublication{
		trackPublicationBase: trackPublicationBase{
			track:  track,
			client: client,
		},
	}
	pub.kind.Store(string(kind))
	pub.name.Store(name)
	return pub
}

func (p *LocalTrackPublication) TrackLocal() webrtc.TrackLocal {
	p.lock.RLock()
	defer p.lock.RUnlock()
	if t, ok := p.track.(webrtc.TrackLocal); ok {
		return t
	}
	return nil
}

func (p *LocalTrackPublication) GetSimulcastTrack(quality livekit.VideoQuality) *LocalSampleTrack {
	p.lock.RLock()
	defer p.lock.RUnlock()
	if p.simulcastTracks == nil {
		return nil
	}
	return p.simulcastTracks[quality]
}

func (p *LocalTrackPublication) SetMuted(muted bool) {
	if p.isMuted.Swap(muted) == muted {
		return
	}
	_ = p.client.SendMuteTrack(p.sid.Load(), muted)
}

func (p *LocalTrackPublication) addSimulcastTrack(st *LocalSampleTrack) {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.simulcastTracks == nil {
		p.simulcastTracks = make(map[livekit.VideoQuality]*LocalSampleTrack)
	}
	if st != nil {
		p.simulcastTracks[st.videoLayer.Quality] = st
	}
}

func (p *LocalTrackPublication) setSender(sender *webrtc.RTPSender) {
	p.lock.Lock()
	p.sender = sender
	p.lock.Unlock()
}

type SimulcastTrack struct {
	trackLocal webrtc.TrackLocal
	videoLayer *livekit.VideoLayer
}

func NewSimulcastTrack(trackLocal webrtc.TrackLocal, videoLayer *livekit.VideoLayer) *SimulcastTrack {
	return &SimulcastTrack{
		trackLocal: trackLocal,
		videoLayer: videoLayer,
	}
}

func (t *SimulcastTrack) TrackLocal() webrtc.TrackLocal {
	return t.trackLocal
}

func (t *SimulcastTrack) VideoLayer() *livekit.VideoLayer {
	return t.videoLayer
}

func (t *SimulcastTrack) Quality() livekit.VideoQuality {
	return t.videoLayer.Quality
}

type TrackPublicationOptions struct {
	Name   string
	Source livekit.TrackSource
	// Set dimensions for video
	VideoWidth  int
	VideoHeight int
	// Opus only
	DisableDTX bool
}
