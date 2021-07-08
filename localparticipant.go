package lksdk

import (
	"time"

	livekit "github.com/livekit/server-sdk-go/proto"
	"github.com/pion/webrtc/v3"
	"google.golang.org/protobuf/proto"
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

func (p *LocalParticipant) PublishTrack(track webrtc.TrackLocal, name string) (*LocalTrackPublication, error) {
	kind := KindFromRTPType(track.Kind())
	pub := LocalTrackPublication{
		trackPublicationBase: trackPublicationBase{
			kind:    kind,
			track:   track,
			name:    name,
			isMuted: false,
			client:  p.engine.client,
		},
	}
	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: &livekit.AddTrackRequest{
				Cid:  track.ID(),
				Name: name,
				Type: kind.ProtoType(),
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
	case <-time.After(5 * time.Second):
		return nil, ErrTrackPublishTimeout
	}

	// add transceivers
	pub.transceiver, err = p.engine.publisher.PeerConnection().AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	})
	if err != nil {
		return nil, err
	}
	pub.sid = pubRes.Track.Sid
	p.addPublication(&pub)

	logger.Info("published track", "track", name)

	return &pub, nil
}

func (p *LocalParticipant) PublishData(data []byte, kind livekit.DataPacket_Kind, destinationSids []string) error {
	packet := &livekit.DataPacket{
		Kind: kind,
		Value: &livekit.DataPacket_User{
			User: &livekit.UserPacket{
				// this is enforced on the server side, setting for completeness
				ParticipantSid: p.sid,
				Payload: data,
				DestinationSids: destinationSids,
			},
		},
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
