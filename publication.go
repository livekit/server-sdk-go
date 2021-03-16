package sdk

type TrackPublication struct {
	Kind  TrackKind
	Track Track
	SID   string
	Name  string
}

func (p *TrackPublication) IsSubscribed() bool {
	return p.Track != nil
}
