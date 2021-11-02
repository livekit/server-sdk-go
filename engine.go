package lksdk

import (
	"sync"
	"time"

	livekit "github.com/livekit/protocol/proto"
	"github.com/pion/webrtc/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const reliableDataChannelName = "_reliable"
const lossyDataChannelName = "_lossy"

type RTCEngine struct {
	publisher          *PCTransport
	subscriber         *PCTransport
	client             *SignalClient
	reliableDC         *webrtc.DataChannel
	lossyDC            *webrtc.DataChannel
	reliableDCSub      *webrtc.DataChannel
	lossyDCSub         *webrtc.DataChannel
	lock               sync.Mutex
	trackPublishedChan chan *livekit.TrackPublishedResponse
	subscriberPrimary  bool

	JoinTimeout time.Duration

	// callbacks
	OnDisconnected          func()
	OnMediaTrack            func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)
	OnParticipantUpdate     func([]*livekit.ParticipantInfo)
	OnActiveSpeakersChanged func([]*livekit.SpeakerInfo)
	OnSpeakersChanged       func([]*livekit.SpeakerInfo)
	OnDataReceived          func(userPacket *livekit.UserPacket)
}

func NewRTCEngine() *RTCEngine {
	return &RTCEngine{
		client:             NewSignalClient(),
		trackPublishedChan: make(chan *livekit.TrackPublishedResponse, 1),
		JoinTimeout:        5 * time.Second,
	}
}

func (e *RTCEngine) Join(url string, token string, params *ConnectParams) (*livekit.JoinResponse, error) {
	res, err := e.client.Join(url, token, params)
	if err != nil {
		return nil, err
	}

	if err = e.configure(res); err != nil {
		return nil, err
	}

	// send offer
	if !res.SubscriberPrimary {
		e.publisher.Negotiate()
	}

	if err = e.waitUntilConnected(); err != nil {
		return nil, err
	}
	return res, err
}

func (e *RTCEngine) Close() {
	if e.publisher != nil {
		_ = e.publisher.Close()
	}
	if e.subscriber != nil {
		_ = e.subscriber.Close()
	}

	e.client.Close()
}

func (e *RTCEngine) IsConnected() bool {
	if e.publisher == nil || e.subscriber == nil {
		return false
	}
	if e.subscriberPrimary {
		return e.subscriber.IsConnected()
	}
	return e.publisher.IsConnected()
}

func (e *RTCEngine) TrackPublishedChan() <-chan *livekit.TrackPublishedResponse {
	return e.trackPublishedChan
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

	e.subscriberPrimary = res.SubscriberPrimary

	e.publisher.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			// done
			return
		}
		if err := e.client.SendICECandidate(candidate.ToJSON(), livekit.SignalTarget_PUBLISHER); err != nil {
			logger.Error(err, "could not send ICE candidates for publisher")
		}
	})
	e.subscriber.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			// done
			return
		}
		if err := e.client.SendICECandidate(candidate.ToJSON(), livekit.SignalTarget_SUBSCRIBER); err != nil {
			logger.Error(err, "could not send ICE candidates for subscriber")
		}
	})

	primaryPC := e.publisher.pc
	if res.SubscriberPrimary {
		primaryPC = e.subscriber.pc
	}
	primaryPC.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
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
		go func() {
			// read incoming rtcp packets so interceptors can handle NACKs
			rtcpBuf := make([]byte, 1500)
			for {
				if _, _, rtcpErr := receiver.Read(rtcpBuf); rtcpErr != nil {
					// pipe closed
					return
				}
			}
		}()
		if e.OnMediaTrack != nil {
			e.OnMediaTrack(remote, receiver)
		}
	})

	e.subscriber.pc.OnDataChannel(func(c *webrtc.DataChannel) {
		if c.Label() == reliableDataChannelName {
			e.reliableDCSub = c
		} else if c.Label() == lossyDataChannelName {
			e.lossyDCSub = c
		} else {
			return
		}
		c.OnMessage(e.handleDataPacket)
	})

	e.publisher.OnOffer = func(offer webrtc.SessionDescription) {
		if err := e.client.SendOffer(offer); err != nil {
			logger.Error(err, "could not send offer")
		}
	}

	trueVal := true
	maxRetries := uint16(1)
	e.lossyDC, err = e.publisher.PeerConnection().CreateDataChannel(lossyDataChannelName, &webrtc.DataChannelInit{
		Ordered:        &trueVal,
		MaxRetransmits: &maxRetries,
	})
	if err != nil {
		return err
	}
	e.lossyDC.OnMessage(e.handleDataPacket)
	e.reliableDC, err = e.publisher.PeerConnection().CreateDataChannel(reliableDataChannelName, &webrtc.DataChannelInit{
		Ordered: &trueVal,
	})
	if err != nil {
		return err
	}
	e.reliableDC.OnMessage(e.handleDataPacket)

	// configure client
	e.client.OnAnswer = func(sd webrtc.SessionDescription) {
		if err := e.publisher.SetRemoteDescription(sd); err != nil {
			logger.Error(err, "could not set remote description")
		} else {
			logger.Info("successfully set publisher answer")
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
			logger.Error(err, "could not set subscriber local description")
			return
		}
		if err := e.client.SendAnswer(answer); err != nil {
			logger.Error(err, "could not send answer for subscriber")
		}
	}
	e.client.OnParticipantUpdate = e.OnParticipantUpdate
	e.client.OnSpeakersChanged = e.OnSpeakersChanged
	e.client.OnLocalTrackPublished = e.handleLocalTrackPublished
	e.client.OnLeave = e.OnDisconnected
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

func (e *RTCEngine) ensurePublisherConnected() error {
	if !e.subscriberPrimary {
		return e.waitUntilConnected()
	}

	if e.publisher.IsConnected() {
		return nil
	}

	e.publisher.Negotiate()

	timeout := time.After(e.JoinTimeout)
	for {
		select {
		case <-timeout:
			return ErrConnectionTimeout
		case <-time.After(10 * time.Millisecond):
			if e.publisher.IsConnected() {
				return nil
			}
		}
	}
}

func (e *RTCEngine) handleLocalTrackPublished(res *livekit.TrackPublishedResponse) {
	e.trackPublishedChan <- res
}

func (e *RTCEngine) handleDataPacket(msg webrtc.DataChannelMessage) {
	packet, err := e.readDataPacket(msg)
	if err != nil {
		return
	}
	switch msg := packet.Value.(type) {
	case *livekit.DataPacket_Speaker:
		if e.OnActiveSpeakersChanged != nil {
			e.OnActiveSpeakersChanged(msg.Speaker.Speakers)
		}
	case *livekit.DataPacket_User:
		if e.OnDataReceived != nil {
			e.OnDataReceived(msg.User)
		}
	}
}

func (e *RTCEngine) readDataPacket(msg webrtc.DataChannelMessage) (*livekit.DataPacket, error) {
	dataPacket := &livekit.DataPacket{}
	if msg.IsString {
		err := protojson.Unmarshal(msg.Data, dataPacket)
		return dataPacket, err
	}
	err := proto.Unmarshal(msg.Data, dataPacket)
	return dataPacket, err
}
