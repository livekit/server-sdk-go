package lksdk

import (
	"sort"
	"time"

	"github.com/pion/webrtc/v3"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
)

const (
	trackPublishTimeout = 10 * time.Second
)

type LocalParticipant struct {
	baseParticipant
	engine *RTCEngine
}

func newLocalParticipant(engine *RTCEngine, roomcallback *RoomCallback) *LocalParticipant {
	return &LocalParticipant{
		baseParticipant: *newBaseParticipant(roomcallback),
		engine:          engine,
	}
}

func (p *LocalParticipant) PublishTrack(track webrtc.TrackLocal, opts *TrackPublicationOptions) (*LocalTrackPublication, error) {
	if opts == nil {
		opts = &TrackPublicationOptions{}
	}
	kind := KindFromRTPType(track.Kind())
	// default sources, since clients generally look for camera/mic
	if opts.Source == livekit.TrackSource_UNKNOWN {
		if kind == TrackKindVideo {
			opts.Source = livekit.TrackSource_CAMERA
		} else if kind == TrackKindAudio {
			opts.Source = livekit.TrackSource_MICROPHONE
		}
	}

	pub := NewLocalTrackPublication(kind, track, opts.Name, p.engine.client)

	req := &livekit.AddTrackRequest{
		Cid:        track.ID(),
		Name:       opts.Name,
		Source:     opts.Source,
		Type:       kind.ProtoType(),
		Width:      uint32(opts.VideoWidth),
		Height:     uint32(opts.VideoHeight),
		DisableDtx: opts.DisableDTX,
	}
	if kind == TrackKindVideo {
		// single layer
		req.Layers = []*livekit.VideoLayer{
			{
				Quality: livekit.VideoQuality_HIGH,
				Width:   uint32(opts.VideoWidth),
				Height:  uint32(opts.VideoHeight),
			},
		}
	}
	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: req,
		},
	})
	if err != nil {
		return nil, err
	}

	pubChan := p.engine.TrackPublishedChan()
	var pubRes *livekit.TrackPublishedResponse

	select {
	case pubRes = <-pubChan:
		break
	case <-time.After(trackPublishTimeout):
		return nil, ErrTrackPublishTimeout
	}

	// add transceivers
	transceiver, err := p.engine.publisher.PeerConnection().AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	})
	if err != nil {
		return nil, err
	}

	pub.setSender(transceiver.Sender())

	pub.sid.Store(pubRes.Track.Sid)
	p.addPublication(pub)

	p.engine.publisher.Negotiate()

	logger.Info("published track", "name", opts.Name, "source", opts.Source.String())

	return pub, nil
}

// PublishSimulcastTrack publishes up to three layers to the server
func (p *LocalParticipant) PublishSimulcastTrack(tracks []*LocalSampleTrack, opts *TrackPublicationOptions) (*LocalTrackPublication, error) {
	if len(tracks) == 0 {
		return nil, nil
	}

	for _, track := range tracks {
		if track.Kind() != webrtc.RTPCodecTypeVideo {
			return nil, ErrUnsupportedSimulcastKind
		}
		if track.videoLayer == nil || track.RID() == "" {
			return nil, ErrInvalidSimulcastTrack
		}
	}

	// tracks should be low to high
	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].videoLayer.Width < tracks[j].videoLayer.Width
	})

	if opts == nil {
		opts = &TrackPublicationOptions{}
	}
	// default sources, since clients generally look for camera/mic
	if opts.Source == livekit.TrackSource_UNKNOWN {
		opts.Source = livekit.TrackSource_CAMERA
	}

	mainTrack := tracks[len(tracks)-1]

	pub := NewLocalTrackPublication(KindFromRTPType(mainTrack.Kind()), nil, opts.Name, p.engine.client)

	var layers []*livekit.VideoLayer
	for _, st := range tracks {
		layers = append(layers, st.videoLayer)
	}
	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: &livekit.AddTrackRequest{
				Cid:    mainTrack.ID(),
				Name:   opts.Name,
				Source: opts.Source,
				Type:   pub.Kind().ProtoType(),
				Width:  mainTrack.videoLayer.Width,
				Height: mainTrack.videoLayer.Height,
				Layers: layers,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	pubChan := p.engine.TrackPublishedChan()
	var pubRes *livekit.TrackPublishedResponse

	select {
	case pubRes = <-pubChan:
		break
	case <-time.After(trackPublishTimeout):
		return nil, ErrTrackPublishTimeout
	}

	// add transceivers
	publishPC := p.engine.publisher.PeerConnection()
	var transceiver *webrtc.RTPTransceiver
	var sender *webrtc.RTPSender
	for idx, st := range tracks {
		if idx == 0 {
			transceiver, err = publishPC.AddTransceiverFromTrack(st, webrtc.RTPTransceiverInit{
				Direction: webrtc.RTPTransceiverDirectionSendonly,
			})
			if err != nil {
				return nil, err
			}
			sender = transceiver.Sender()
			pub.setSender(sender)
		} else {
			if err = sender.AddEncoding(st); err != nil {
				return nil, err
			}
		}
		pub.addSimulcastTrack(st)
		st.SetTransceiver(transceiver)
	}

	pub.sid.Store(pubRes.Track.Sid)
	pub.updateInfo(pubRes.Track)
	p.addPublication(pub)

	p.engine.publisher.Negotiate()

	logger.Info("published simulcast track", "name", opts.Name, "source", opts.Source.String())

	return pub, nil
}

func (p *LocalParticipant) PublishData(data []byte, kind livekit.DataPacket_Kind, destinationSids []string) error {
	packet := &livekit.DataPacket{
		Kind: kind,
		Value: &livekit.DataPacket_User{
			User: &livekit.UserPacket{
				// this is enforced on the server side, setting for completeness
				ParticipantSid:  p.sid,
				Payload:         data,
				DestinationSids: destinationSids,
			},
		},
	}

	if err := p.engine.ensurePublisherConnected(true); err != nil {
		return err
	}

	// encode packet
	encoded, err := proto.Marshal(packet)
	if err != nil {
		return err
	}

	if kind == livekit.DataPacket_RELIABLE {
		return p.engine.reliableDC.Send(encoded)
	} else if kind == livekit.DataPacket_LOSSY {
		return p.engine.lossyDC.Send(encoded)
	}

	return nil
}

func (p *LocalParticipant) UnpublishTrack(sid string) error {
	obj, loaded := p.tracks.LoadAndDelete(sid)
	if !loaded {
		return ErrCannotFindTrack
	}
	p.audioTracks.Delete(sid)
	p.videoTracks.Delete(sid)

	pub, ok := obj.(*LocalTrackPublication)
	if !ok {
		return nil
	}

	var err error
	if localTrack, ok := pub.track.(webrtc.TrackLocal); ok {
		for _, sender := range p.engine.publisher.pc.GetSenders() {
			if sender.Track() == localTrack {
				err = p.engine.publisher.pc.RemoveTrack(sender)
				break
			}
		}
		p.engine.publisher.Negotiate()
	}

	return err
}

// GetSubscriberPeerConnection is a power-user API that gives access to the underlying subscriber peer connection
// subscribed tracks are received using this PeerConnection
func (p *LocalParticipant) GetSubscriberPeerConnection() *webrtc.PeerConnection {
	return p.engine.subscriber.PeerConnection()
}

// GetPublisherPeerConnection is a power-user API that gives access to the underlying publisher peer connection
// local tracks are published to server via this PeerConnection
func (p *LocalParticipant) GetPublisherPeerConnection() *webrtc.PeerConnection {
	return p.engine.publisher.PeerConnection()
}

func (p *LocalParticipant) updateInfo(info *livekit.ParticipantInfo) {
	p.baseParticipant.updateInfo(info, p)

	// detect tracks that have been muted remotely, and apply changes
	for _, ti := range info.Tracks {
		pub := p.getLocalPublication(ti.Sid)
		if pub == nil {
			continue
		}
		if pub.IsMuted() != ti.Muted {
			pub.SetMuted(ti.Muted)

			// trigger callback
			if ti.Muted {
				p.Callback.OnTrackMuted(pub, p)
				p.roomCallback.OnTrackMuted(pub, p)
			} else if !ti.Muted {
				p.Callback.OnTrackUnmuted(pub, p)
				p.roomCallback.OnTrackUnmuted(pub, p)
			}
		}
	}
}

func (p *LocalParticipant) getLocalPublication(sid string) *LocalTrackPublication {
	if pub, ok := p.getPublication(sid).(*LocalTrackPublication); ok {
		return pub
	}
	return nil
}
