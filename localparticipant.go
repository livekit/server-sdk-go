package sdk

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
