package lksdk

import (
	"github.com/pion/webrtc/v3"
)

type TrackKind string

const (
	TrackKindVideo TrackKind = "video"
	TrackKindAudio TrackKind = "audio"
	TrackKindData  TrackKind = "data"
)

type Track interface {
	Kind() TrackKind
	Name() string
}

type trackBase struct {
	kind TrackKind
	name string
}

func (t *trackBase) Name() string {
	return t.name
}

func (t *trackBase) Kind() TrackKind {
	return t.kind
}

type LocalMediaTrack struct {
	trackBase
}

type LocalDataTrack struct {
	trackBase
}

type RemoteMediaTrack struct {
	mediaTrack *webrtc.TrackRemote
	trackBase
}

type RemoteDataTrack struct {
	trackBase
}
