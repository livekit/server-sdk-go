package lksdk

import (
	"time"

	"github.com/pion/webrtc/v3"

	livekit "github.com/livekit/livekit-sdk-go/proto"
)

type RemoteParticipant struct {
	baseParticipant
}

func newRemoteParticipant(pi *livekit.ParticipantInfo, roomCallback *RoomCallback) *RemoteParticipant {
	p := &RemoteParticipant{
		baseParticipant: *newBaseParticipant(roomCallback),
	}
	p.updateInfo(pi)
	return p
}

func (p *RemoteParticipant) updateInfo(pi *livekit.ParticipantInfo) {
	hadInfo := p.info != nil
	p.baseParticipant.updateInfo(pi, p)
	// update tracks
	validPubs := make(map[string]TrackPublication, len(p.tracks))
	newPubs := make(map[string]TrackPublication)

	for _, ti := range pi.Tracks {
		pub := p.getPublication(ti.Sid)
		if pub == nil {
			// new track
			remotePub := &RemoteTrackPublication{}
			remotePub.updateInfo(ti)
			p.addPublication(remotePub)
			newPubs[ti.Sid] = remotePub
			pub = remotePub
		} else {
			wasMuted := pub.IsMuted()
			pub.updateInfo(ti)
			if ti.Muted != wasMuted {
				if ti.Muted {
					p.Callback.OnTrackMuted(pub, p)
					p.roomCallback.OnTrackMuted(pub, p)
				} else {
					p.Callback.OnTrackUnmuted(pub, p)
					p.roomCallback.OnTrackUnmuted(pub, p)
				}
			}
		}
		validPubs[ti.Sid] = pub
	}

	if hadInfo {
		// send events for new publications
		for _, pub := range newPubs {
			p.Callback.OnTrackPublished(pub, p)
			p.roomCallback.OnTrackPublished(pub, p)
		}
	}

	p.lock.Lock()
	var toUnpublish []string
	for sid := range p.tracks {
		if validPubs[sid] == nil {
			toUnpublish = append(toUnpublish, sid)
		}
	}
	p.lock.Unlock()

	for _, sid := range toUnpublish {
		p.unpublishTrack(sid, true)
	}
}

func (p *RemoteParticipant) addSubscribedMediaTrack(track *webrtc.TrackRemote, trackSID string,
	receiver *webrtc.RTPReceiver) {
	pub := p.getPublication(trackSID)
	if pub == nil {
		// wait for metadata to arrive
		go func() {
			start := time.Now()
			for time.Now().Sub(start) < 5*time.Second {
				pub := p.getPublication(trackSID)
				if pub != nil {
					p.addSubscribedMediaTrack(track, trackSID, receiver)
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
			p.Callback.OnTrackSubscriptionFailed(trackSID, p)
			p.roomCallback.OnTrackSubscriptionFailed(trackSID, p)
		}()
		return
	}

	pub.track = track
	pub.receiver = receiver

	logger.Info("track subscribed",
		"participant", p.Identity(), "track", pub.sid,
		"kind", pub.kind)
	p.Callback.OnTrackSubscribed(track, pub, p)
	p.roomCallback.OnTrackSubscribed(track, pub, p)
}

func (p *RemoteParticipant) getPublication(trackSID string) *RemoteTrackPublication {
	if pub, ok := p.baseParticipant.getPublication(trackSID).(*RemoteTrackPublication); ok {
		return pub
	}
	return nil
}

func (p *RemoteParticipant) unpublishTrack(sid string, sendUnpublish bool) {
	pub := p.getPublication(sid)
	if pub == nil {
		return
	}

	p.lock.Lock()
	defer p.lock.Unlock()
	switch pub.Kind() {
	case TrackKindAudio:
		delete(p.audioTracks, sid)
	case TrackKindVideo:
		delete(p.videoTracks, sid)
	}

	track := pub.TrackRemote()
	if track != nil {
		p.Callback.OnTrackUnsubscribed(track, pub, p)
		p.roomCallback.OnTrackUnsubscribed(track, pub, p)
	}
	if sendUnpublish {
		p.Callback.OnTrackUnpublished(pub, p)
		p.roomCallback.OnTrackUnpublished(pub, p)
	}
}

func (p *RemoteParticipant) unpublishAllTracks() {
	p.lock.Lock()
	defer p.lock.Unlock()

	for _, pub := range p.tracks {
		// subscribed tracks, send unsubscribe events
		if remoteTrack, ok := pub.Track().(*webrtc.TrackRemote); ok {
			if pub.Track() != nil {
				p.Callback.OnTrackUnsubscribed(remoteTrack, pub, p)
				p.roomCallback.OnTrackUnsubscribed(remoteTrack, pub, p)
			}
		}
	}
	p.tracks = newTrackMap()
	p.audioTracks = newTrackMap()
	p.videoTracks = newTrackMap()
}
