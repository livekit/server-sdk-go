package lksdk

import (
	livekit "github.com/livekit/livekit-sdk-go/proto"
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
