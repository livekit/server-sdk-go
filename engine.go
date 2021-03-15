package sdk

import (
	"time"

	"github.com/pion/webrtc/v3"

	livekit "github.com/livekit/livekit-sdk-go/proto"
)

const privateDataChanName = "_private"

type RTCEngine struct {
	publisher  *PCTransport
	subscriber *PCTransport
	client     *SignalClient
	privateDC  *webrtc.DataChannel

	JoinTimeout time.Duration

	// callbacks
	OnDisconnected          func()
	OnMediaTrack            func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)
	OnDataChannel           func(channel *webrtc.DataChannel)
	OnParticipantUpdate     func([]*livekit.ParticipantInfo)
	OnLocalTrackPublished   func(response *livekit.TrackPublishedResponse)
	OnActiveSpeakersChanged func([]*livekit.SpeakerInfo)
}

func NewRTCEngine() *RTCEngine {
	return &RTCEngine{
		client:      NewSignalClient(),
		JoinTimeout: 5 * time.Second,
	}
}

func (e *RTCEngine) Join(url string, info ConnectInfo) (*livekit.JoinResponse, error) {
	res, err := e.client.Join(url, info)
	if err := e.configure(res); err != nil {
		return nil, err
	}

	if err := e.waitUntilConnected(); err != nil {
		return nil, err
	}
	return res, err
}

func (e *RTCEngine) JoinWithToken(url string, token string) (*livekit.JoinResponse, error) {
	res, err := e.client.JoinWithToken(url, token)
	if err := e.configure(res); err != nil {
		return nil, err
	}

	if err := e.waitUntilConnected(); err != nil {
		return nil, err
	}
	return res, err
}

func (e *RTCEngine) IsConnected() bool {
	if e.publisher == nil {
		return false
	}
	return e.publisher.IsConnected()
}

func (e *RTCEngine) configure(res *livekit.JoinResponse) error {
	iceServers := FromProtoIceServers(res.IceServers)
	var err error
	if e.publisher, err = NewPCTransport(iceServers); err != nil {
		return err
	}
	if e.subscriber, err = NewPCTransport(iceServers); err != nil {
		return err
	}

	e.publisher.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		e.client.SendICECandidate(candidate.ToJSON(), livekit.SignalTarget_PUBLISHER)
	})
	e.subscriber.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		e.client.SendICECandidate(candidate.ToJSON(), livekit.SignalTarget_SUBSCRIBER)
	})

	e.publisher.OnNegotiationNeeded(func() {
		if e.publisher.pc.RemoteDescription() == nil {
			return
		}
		e.negotiate()
	})

	e.publisher.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		switch state {
		case webrtc.ICEConnectionStateConnected:
			logger.Info("ICE connected")
		case webrtc.ICEConnectionStateDisconnected:
			logger.Info("ICE disconnected")
			if e.OnDisconnected != nil {
				e.OnDisconnected()
			}
		}
	})

	e.subscriber.pc.OnTrack(func(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if e.OnMediaTrack != nil {
			e.OnMediaTrack(remote, receiver)
		}
	})

	e.subscriber.pc.OnDataChannel(func(channel *webrtc.DataChannel) {
		if e.OnDataChannel != nil {
			e.OnDataChannel(channel)
		}
	})

	e.privateDC, err = e.publisher.pc.CreateDataChannel(privateDataChanName, nil)
	if err != nil {
		return err
	}

	// configure client
	e.client.OnAnswer = func(sd webrtc.SessionDescription) {
		if err := e.publisher.SetRemoteDescription(sd); err != nil {
			logger.Error(err, "could not set remote description")
		}
	}
	e.client.OnTrickle = func(init webrtc.ICECandidateInit, target livekit.SignalTarget) {
		var err error
		if target == livekit.SignalTarget_PUBLISHER {
			err = e.publisher.AddICECandidate(init)
		} else if target == livekit.SignalTarget_SUBSCRIBER {
			err = e.subscriber.AddICECandidate(init)
		}
		if err != nil {
			logger.Error(err, "could not add ICE candidate")
		}
	}
	e.client.OnOffer = func(sd webrtc.SessionDescription) {
		logger.Info("received offer for subscriber")
		if err := e.subscriber.SetRemoteDescription(sd); err != nil {
			logger.Error(err, "could not set remote description")
			return
		}
		answer, err := e.subscriber.pc.CreateAnswer(nil)
		if err != nil {
			logger.Error(err, "could not create answer")
			return
		}
		if err := e.subscriber.pc.SetLocalDescription(answer); err != nil {
			logger.Error(err, "could not set subscriber localdescription")
			return
		}
		e.client.SendAnswer(answer)
	}
	e.client.OnParticipantUpdate = e.OnParticipantUpdate
	e.client.OnActiveSpeakersChanged = e.OnActiveSpeakersChanged
	e.client.OnLocalTrackPublished = e.OnLocalTrackPublished
	e.client.OnClose = func() {
		// TODO: implement reconnection logic
		logger.Info("signal connection disconnected")
	}
	return nil
}

func (e *RTCEngine) waitUntilConnected() error {
	timeout := time.After(e.JoinTimeout)
	for {
		select {
		case <-timeout:
			return ErrConnectionTimeout
		case <-time.After(10 * time.Millisecond):
			if e.IsConnected() {
				return nil
			}
		}
	}
}

func (e *RTCEngine) negotiate() {
	logger.Info("starting to negotiate")
	offer, err := e.publisher.pc.CreateOffer(nil)
	if err != nil {
		logger.Error(err, "failed to negotiate")
		return
	}
	if err := e.publisher.pc.SetLocalDescription(offer); err != nil {
		logger.Error(err, "could not set local description")
		return
	}
	if err := e.client.SendOffer(offer); err != nil {
		logger.Error(err, "could not send offer")
		return
	}
}
