package lksdk

import (
	livekit "github.com/livekit/livekit-sdk-go/proto"
)

type LocalParticipant struct {
	baseParticipant
	engine *RTCEngine
}

func newLocalParticipant(engine *RTCEngine) *LocalParticipant {
	return &LocalParticipant{
		baseParticipant: *newBaseParticipant(),
		engine:          engine,
	}
}

func (p *LocalParticipant) updateInfo(info *livekit.ParticipantInfo) {
	p.baseParticipant.updateInfo(info, p)

}
