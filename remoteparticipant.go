package lksdk

import (
	"github.com/pion/webrtc/v3"

	livekit "github.com/livekit/livekit-sdk-go/proto"
)

type RemoteParticipant struct {
	baseParticipant

	onTrackSubscribed         func(track Track, publication *TrackPublication)
	onTrackUnsubscribed       func(track Track, publication *TrackPublication)
	onTrackSubscriptionFailed func(sid string)
	onTrackPublished          func(publication *TrackPublication)
	onTrackUnpublished        func(publication *TrackPublication)
	onTrackMessage            func(msg webrtc.DataChannelMessage)
	onTrackMuted              func(track Track, pub *TrackPublication)
	onTrackUnmuted            func(track Track, pub *TrackPublication)
}

func newRemoteParticipant(pi *livekit.ParticipantInfo) *RemoteParticipant {
	p := &RemoteParticipant{
		baseParticipant: *newBaseParticipant(),
	}
	p.updateMetadata(pi)
	return p
}

func (p *RemoteParticipant) unpublishTracks() {
	p.lock.Lock()
	defer p.lock.Unlock()

	for _, pub := range p.tracks {
		switch t := pub.Track.(type) {
		case *RemoteMediaTrack, *RemoteDataTrack:
			// subscribed tracks, send unsubscribe events
			if p.onTrackUnsubscribed != nil {
				p.onTrackUnsubscribed(t, pub)
			}
		}
	}
	p.tracks = newTrackMap()
	p.audioTracks = newTrackMap()
	p.videoTracks = newTrackMap()
	p.dataTracks = newTrackMap()
}
