package lksdk

import (
	"time"

	"github.com/pion/webrtc/v3"

	livekit "github.com/livekit/livekit-sdk-go/proto"
)

type RemoteParticipant struct {
	baseParticipant

	onTrackSubscribed         func(track *webrtc.TrackRemote, publication TrackPublication, rp *RemoteParticipant)
	onTrackUnsubscribed       func(track *webrtc.TrackRemote, publication TrackPublication, rp *RemoteParticipant)
	onTrackSubscriptionFailed func(sid string, rp *RemoteParticipant)
	onTrackPublished          func(publication TrackPublication, rp *RemoteParticipant)
	onTrackUnpublished        func(publication TrackPublication, rp *RemoteParticipant)
	onTrackMessage            func(msg webrtc.DataChannelMessage, rp *RemoteParticipant)
}

func newRemoteParticipant(pi *livekit.ParticipantInfo) *RemoteParticipant {
	p := &RemoteParticipant{
		baseParticipant: *newBaseParticipant(),
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
			pub.updateInfo(ti)
		}
		validPubs[ti.Sid] = pub
	}

	if hadInfo && p.onTrackPublished != nil {
		// send events for new publications
		for _, pub := range newPubs {
			p.onTrackPublished(pub, p)
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
			foundPub := false
			start := time.Now()
			for time.Now().Sub(start) < 5*time.Second {
				pub := p.getPublication(trackSID)
				if pub != nil {
					foundPub = true
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			if !foundPub {
				if p.onTrackSubscriptionFailed != nil {
					p.onTrackSubscriptionFailed(trackSID, p)
				}
			}
		}()
		return
	}

	pub.track = track
	pub.receiver = receiver

	if p.onTrackSubscribed != nil {
		p.onTrackSubscribed(track, pub, p)
	}
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
	if track != nil && p.onTrackUnsubscribed != nil {
		p.onTrackUnsubscribed(track, pub, p)
	}
	if sendUnpublish && p.onTrackUnpublished != nil {
		p.onTrackUnpublished(pub, p)
	}
}

func (p *RemoteParticipant) unpublishAllTracks() {
	p.lock.Lock()
	defer p.lock.Unlock()

	for _, pub := range p.tracks {
		// subscribed tracks, send unsubscribe events
		if remoteTrack, ok := pub.Track().(*webrtc.TrackRemote); ok {
			if pub.Track() != nil && p.onTrackUnsubscribed != nil {
				p.onTrackUnsubscribed(remoteTrack, pub, p)
			}
		}
	}
	p.tracks = newTrackMap()
	p.audioTracks = newTrackMap()
	p.videoTracks = newTrackMap()
}
