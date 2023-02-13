package lksdk

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
)

const (
	reliableDataChannelName = "_reliable"
	lossyDataChannelName    = "_lossy"

	maxReconnectCount        = 10
	initialReconnectInterval = 300 * time.Millisecond
	maxReconnectInterval     = 60 * time.Second
)

type RTCEngine struct {
	publisher          *PCTransport
	subscriber         *PCTransport
	client             *SignalClient
	dclock             sync.RWMutex
	reliableDC         *webrtc.DataChannel
	lossyDC            *webrtc.DataChannel
	reliableDCSub      *webrtc.DataChannel
	lossyDCSub         *webrtc.DataChannel
	trackPublishedChan chan *livekit.TrackPublishedResponse
	subscriberPrimary  bool
	hasConnected       atomic.Bool
	hasPublish         atomic.Bool
	closed             atomic.Bool
	reconnecting       atomic.Bool

	url        string
	token      atomic.String
	connParams *ConnectParams

	JoinTimeout time.Duration

	// callbacks
	OnDisconnected          func()
	OnMediaTrack            func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)
	OnParticipantUpdate     func([]*livekit.ParticipantInfo)
	OnActiveSpeakersChanged func([]*livekit.SpeakerInfo)
	OnSpeakersChanged       func([]*livekit.SpeakerInfo)
	OnDataReceived          func(userPacket *livekit.UserPacket)
	OnConnectionQuality     func([]*livekit.ConnectionQualityInfo)
	OnRoomUpdate            func(room *livekit.Room)
	OnRestarting            func()
	OnRestarted             func(*livekit.JoinResponse)
	OnResuming              func()
	OnResumed               func()
}

func NewRTCEngine() *RTCEngine {
	e := &RTCEngine{
		client:             NewSignalClient(),
		trackPublishedChan: make(chan *livekit.TrackPublishedResponse, 1),
		JoinTimeout:        15 * time.Second,
	}

	e.client.OnParticipantUpdate = func(info []*livekit.ParticipantInfo) {
		if f := e.OnParticipantUpdate; f != nil {
			f(info)
		}
	}
	e.client.OnSpeakersChanged = func(si []*livekit.SpeakerInfo) {
		if f := e.OnSpeakersChanged; f != nil {
			f(si)
		}
	}
	e.client.OnLocalTrackPublished = e.handleLocalTrackPublished
	e.client.OnConnectionQuality = func(cqi []*livekit.ConnectionQualityInfo) {
		if f := e.OnConnectionQuality; f != nil {
			f(cqi)
		}
	}
	e.client.OnRoomUpdate = func(room *livekit.Room) {
		if f := e.OnRoomUpdate; f != nil {
			f(room)
		}
	}
	e.client.OnLeave = e.handleLeave
	e.client.OnTokenRefresh = func(refreshToken string) {
		e.token.Store(refreshToken)
	}
	e.client.OnClose = func() { e.handleDisconnect(false) }

	return e
}

func (e *RTCEngine) Join(url string, token string, params *ConnectParams) (*livekit.JoinResponse, error) {
	res, err := e.client.Join(url, token, params)
	if err != nil {
		return nil, err
	}

	e.url = url
	e.token.Store(token)
	e.connParams = params

	if err = e.configure(res); err != nil {
		return nil, err
	}

	e.client.Start()

	// send offer
	if !res.SubscriberPrimary {
		e.publisher.Negotiate()
	}

	if err = e.waitUntilConnected(); err != nil {
		return nil, err
	}
	e.hasConnected.Store(true)
	return res, err
}

func (e *RTCEngine) Close() {
	if !e.closed.CompareAndSwap(false, true) {
		return
	}
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

func (e *RTCEngine) setRTT(rtt uint32) {
	if pc := e.publisher; pc != nil {
		pc.SetRTT(rtt)
	}

	if pc := e.subscriber; pc != nil {
		pc.SetRTT(rtt)
	}
}

func (e *RTCEngine) configure(res *livekit.JoinResponse) error {
	iceServers := FromProtoIceServers(res.IceServers)
	configuration := webrtc.Configuration{ICEServers: iceServers}
	if res.GetClientConfiguration().GetForceRelay() == livekit.ClientConfigSetting_ENABLED {
		configuration.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	var err error
	if e.publisher, err = NewPCTransport(configuration); err != nil {
		return err
	}
	if e.subscriber, err = NewPCTransport(configuration); err != nil {
		return err
	}

	e.subscriberPrimary = res.SubscriberPrimary
	e.subscriber.OnRemoteDescriptionSettled(e.createPublisherAnswerAndSend)

	e.publisher.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			// done
			return
		}
		if err := e.client.SendICECandidate(candidate.ToJSON(), livekit.SignalTarget_PUBLISHER); err != nil {
			logger.Errorw("could not send ICE candidates for publisher", err)
		}
	})
	e.subscriber.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			// done
			return
		}
		if err := e.client.SendICECandidate(candidate.ToJSON(), livekit.SignalTarget_SUBSCRIBER); err != nil {
			logger.Errorw("could not send ICE candidates for subscriber", err)
		}
	})

	primaryPC := e.publisher.pc
	if res.SubscriberPrimary {
		primaryPC = e.subscriber.pc
	}
	primaryPC.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		switch state {
		case webrtc.ICEConnectionStateConnected:
			logger.Infow("ICE connected")
		case webrtc.ICEConnectionStateDisconnected:
			logger.Infow("ICE disconnected")
		case webrtc.ICEConnectionStateFailed:
			logger.Infow("ICE failed")
			e.handleDisconnect(false)
		}
	})

	e.subscriber.pc.OnTrack(func(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if e.OnMediaTrack != nil {
			e.OnMediaTrack(remote, receiver)
		}
	})

	e.subscriber.pc.OnDataChannel(func(c *webrtc.DataChannel) {
		e.dclock.Lock()
		defer e.dclock.Unlock()
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
			logger.Errorw("could not send offer", err)
		}
	}

	trueVal := true
	maxRetries := uint16(1)
	e.dclock.Lock()
	e.lossyDC, err = e.publisher.PeerConnection().CreateDataChannel(lossyDataChannelName, &webrtc.DataChannelInit{
		Ordered:        &trueVal,
		MaxRetransmits: &maxRetries,
	})
	if err != nil {
		e.dclock.Unlock()
		return err
	}
	e.lossyDC.OnMessage(e.handleDataPacket)
	e.reliableDC, err = e.publisher.PeerConnection().CreateDataChannel(reliableDataChannelName, &webrtc.DataChannelInit{
		Ordered: &trueVal,
	})
	if err != nil {
		e.dclock.Unlock()
		return err
	}
	e.reliableDC.OnMessage(e.handleDataPacket)
	e.dclock.Unlock()

	// configure client
	e.client.OnAnswer = func(sd webrtc.SessionDescription) {
		if err := e.publisher.SetRemoteDescription(sd); err != nil {
			logger.Errorw("could not set remote description", err)
		} else {
			logger.Infow("successfully set publisher answer")
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
			logger.Errorw("could not add ICE candidate", err)
		}
	}
	e.client.OnOffer = func(sd webrtc.SessionDescription) {
		logger.Infow("received offer for subscriber")
		if err := e.subscriber.SetRemoteDescription(sd); err != nil {
			logger.Errorw("could not set remote description", err)
			return
		}

	}
	return nil
}

func (e *RTCEngine) GetDataChannel(kind livekit.DataPacket_Kind) *webrtc.DataChannel {
	e.dclock.RLock()
	defer e.dclock.RUnlock()
	if kind == livekit.DataPacket_RELIABLE {
		return e.reliableDC
	}
	return e.lossyDC
}

func (e *RTCEngine) GetDataChannelSub(kind livekit.DataPacket_Kind) *webrtc.DataChannel {
	e.dclock.RLock()
	defer e.dclock.RUnlock()
	if kind == livekit.DataPacket_RELIABLE {
		return e.reliableDCSub
	}
	return e.lossyDCSub
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

func (e *RTCEngine) ensurePublisherConnected(ensureDataReady bool) error {
	if !e.subscriberPrimary {
		return e.waitUntilConnected()
	}

	if e.publisher.IsConnected() && (!ensureDataReady || e.dataPubChannelReady()) {
		return nil
	}

	e.hasPublish.Store(true)

	e.publisher.Negotiate()

	timeout := time.After(e.JoinTimeout)
	for {
		select {
		case <-timeout:
			return ErrConnectionTimeout
		case <-time.After(10 * time.Millisecond):
			if e.publisher.IsConnected() {
				if !ensureDataReady || e.dataPubChannelReady() {
					return nil
				}
			}
		}
	}
}

func (e *RTCEngine) dataPubChannelReady() bool {
	e.dclock.RLock()
	defer e.dclock.RUnlock()
	return e.reliableDC.ReadyState() == webrtc.DataChannelStateOpen && e.lossyDC.ReadyState() == webrtc.DataChannelStateOpen
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

func (e *RTCEngine) handleDisconnect(fullReconnect bool) {
	// do not retry until fully connected
	if e.closed.Load() || !e.hasConnected.Load() {
		return
	}

	if !e.reconnecting.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer e.reconnecting.Store(false)
		var reconnectCount int
		for ; reconnectCount < maxReconnectCount; reconnectCount++ {
			if fullReconnect {
				if reconnectCount == 0 && e.OnRestarting != nil {
					e.OnRestarting()
				}
				logger.Infow("restarting connection...", "reconnectCount", reconnectCount)
				if err := e.restartConnection(); err != nil {
					logger.Errorw("restart connection failed", err)
				} else {
					return
				}
			} else {
				if reconnectCount == 0 && e.OnResuming != nil {
					e.OnResuming()
				}
				logger.Infow("resuming connection...", "reconnectCount", reconnectCount)
				if err := e.resumeConnection(); err != nil {
					logger.Errorw("resume connection failed", err)
					fullReconnect = true
				} else {
					return
				}
			}

			delay := time.Duration(reconnectCount*reconnectCount) * initialReconnectInterval
			if delay > maxReconnectInterval {
				break
			}
			if reconnectCount < maxReconnectCount-1 {
				time.Sleep(delay)
			}
		}

		if e.OnDisconnected != nil {
			e.OnDisconnected()
		}
	}()
}

func (e *RTCEngine) resumeConnection() error {
	_, err := e.client.Join(e.url, e.token.Load(), &ConnectParams{Reconnect: true})
	if err != nil {
		return err
	}

	e.client.Start()

	// send offer if publisher enabled
	if !e.subscriberPrimary || e.hasPublish.Load() {
		e.publisher.createAndSendOffer(&webrtc.OfferOptions{
			ICERestart: true,
		})
	}

	if err = e.waitUntilConnected(); err != nil {
		return err
	}

	if e.OnResumed != nil {
		e.OnResumed()
	}
	return nil
}

func (e *RTCEngine) restartConnection() error {
	if e.client.IsStarted() {
		e.client.SendLeave()
	}
	e.client.Close()
	if e.publisher != nil {
		e.publisher.Close()
	}

	if e.subscriber != nil {
		e.subscriber.Close()
	}

	res, err := e.Join(e.url, e.token.Load(), e.connParams)
	if err != nil {
		return err
	}

	if e.OnRestarted != nil {
		e.OnRestarted(res)
	}
	return nil
}

func (e *RTCEngine) createPublisherAnswerAndSend() error {
	answer, err := e.subscriber.pc.CreateAnswer(nil)
	if err != nil {
		logger.Errorw("could not create answer", err)
		return err
	}
	if err := e.subscriber.pc.SetLocalDescription(answer); err != nil {
		logger.Errorw("could not set subscriber local description", err)
		return err
	}
	if err := e.client.SendAnswer(answer); err != nil {
		logger.Errorw("could not send answer for subscriber", err)
		return err
	}
	return nil
}

func (e *RTCEngine) handleLeave(leave *livekit.LeaveRequest) {
	if leave.GetCanReconnect() {
		e.handleDisconnect(true)
	} else {
		logger.Infow("Leave room", "reason", leave.GetReason())
		if e.OnDisconnected != nil {
			e.OnDisconnected()
		}
	}
}
