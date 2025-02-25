// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lksdk

import (
	"context"
	"sync"
	"time"

	protoLogger "github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
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
	log protoLogger.Logger

	pclock     sync.Mutex
	publisher  *PCTransport
	subscriber *PCTransport
	client     *SignalClient

	dclock        sync.RWMutex
	reliableDC    *webrtc.DataChannel
	lossyDC       *webrtc.DataChannel
	reliableDCSub *webrtc.DataChannel
	lossyDCSub    *webrtc.DataChannel

	trackPublishedListenersLock sync.Mutex
	trackPublishedListeners     map[string]chan *livekit.TrackPublishedResponse

	subscriberPrimary     bool
	hasConnected          atomic.Bool
	hasPublish            atomic.Bool
	closed                atomic.Bool
	reconnecting          atomic.Bool
	requiresFullReconnect atomic.Bool

	url        string
	token      atomic.String
	connParams *SignalClientConnectParams

	JoinTimeout time.Duration

	// callbacks
	OnLocalTrackUnpublished func(response *livekit.TrackUnpublishedResponse)
	OnTrackRemoteMuted      func(request *livekit.MuteTrackRequest)
	OnDisconnected          func(reason DisconnectionReason)
	OnMediaTrack            func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)
	OnParticipantUpdate     func([]*livekit.ParticipantInfo)
	OnSpeakersChanged       func([]*livekit.SpeakerInfo)
	OnDataReceived          func(userPacket *livekit.UserPacket) // Deprecated: Use OnDataPacket instead
	OnDataPacket            func(identity string, dataPacket DataPacket)
	OnConnectionQuality     func([]*livekit.ConnectionQualityInfo)
	OnRoomUpdate            func(room *livekit.Room)
	OnRestarting            func()
	OnRestarted             func(*livekit.JoinResponse)
	OnResuming              func()
	OnResumed               func()
	OnTranscription         func(*livekit.Transcription)
	OnSignalClientConnected func(*livekit.JoinResponse)

	// callbacks to get data
	CbGetLocalParticipantSID func() string
}

func NewRTCEngine() *RTCEngine {
	e := &RTCEngine{
		log:                     logger,
		client:                  NewSignalClient(),
		trackPublishedListeners: make(map[string]chan *livekit.TrackPublishedResponse),
		JoinTimeout:             15 * time.Second,
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
	e.client.OnLocalTrackUnpublished = e.handleLocalTrackUnpublished
	e.client.OnTrackRemoteMuted = e.handleTrackRemoteMuted
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

// SetLogger overrides default logger.
func (e *RTCEngine) SetLogger(l protoLogger.Logger) {
	e.log = l
	e.client.SetLogger(l)
	if e.publisher != nil {
		e.publisher.SetLogger(l)
	}
	if e.subscriber != nil {
		e.subscriber.SetLogger(l)
	}
}

// Deprecated, use JoinContext.
func (e *RTCEngine) Join(url string, token string, params *SignalClientConnectParams) (*livekit.JoinResponse, error) {
	return e.JoinContext(context.TODO(), url, token, params)
}

func (e *RTCEngine) JoinContext(ctx context.Context, url string, token string, params *SignalClientConnectParams) (*livekit.JoinResponse, error) {
	res, err := e.client.JoinContext(ctx, url, token, *params)
	if err != nil {
		return nil, err
	}

	e.url = url
	e.token.Store(token)
	e.connParams = params

	err = e.configure(res.IceServers, res.ClientConfiguration, proto.Bool(res.SubscriberPrimary))
	if err != nil {
		return nil, err
	}

	if e.OnSignalClientConnected != nil {
		e.OnSignalClientConnected(res)
	}

	e.client.Start()

	// send offer
	if !res.SubscriberPrimary || res.FastPublish {
		if publisher, ok := e.Publisher(); ok {
			publisher.Negotiate()
		} else {
			return nil, ErrNoPeerConnection
		}
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

	go func() {
		for e.reconnecting.Load() {
			time.Sleep(50 * time.Millisecond)
		}

		if publisher, ok := e.Publisher(); ok {
			_ = publisher.Close()
		}
		if subscriber, ok := e.Subscriber(); ok {
			_ = subscriber.Close()
		}

		e.client.Close()
	}()
}

func (e *RTCEngine) IsConnected() bool {
	e.pclock.Lock()
	defer e.pclock.Unlock()

	if e.publisher == nil || e.subscriber == nil {
		return false
	}
	if e.subscriberPrimary {
		return e.subscriber.IsConnected()
	}
	return e.publisher.IsConnected()
}

func (e *RTCEngine) Publisher() (*PCTransport, bool) {
	e.pclock.Lock()
	defer e.pclock.Unlock()
	return e.publisher, e.publisher != nil
}

func (e *RTCEngine) Subscriber() (*PCTransport, bool) {
	e.pclock.Lock()
	defer e.pclock.Unlock()
	return e.subscriber, e.subscriber != nil
}

func (e *RTCEngine) setRTT(rtt uint32) {
	if subscriber, ok := e.Subscriber(); ok {
		subscriber.SetRTT(rtt)
	}
}

func (e *RTCEngine) configure(
	iceServers []*livekit.ICEServer,
	clientConfig *livekit.ClientConfiguration,
	subscriberPrimary *bool) error {

	configuration := e.makeRTCConfiguration(iceServers, clientConfig)
	e.pclock.Lock()
	defer e.pclock.Unlock()

	// remove previous transport
	if e.publisher != nil {
		e.publisher.Close()
		e.publisher = nil
	}
	if e.subscriber != nil {
		e.subscriber.Close()
		e.subscriber = nil
	}

	var err error
	if e.publisher, err = NewPCTransport(PCTransportParams{
		Configuration:        configuration,
		RetransmitBufferSize: e.connParams.RetransmitBufferSize,
		Pacer:                e.connParams.Pacer,
		Interceptors:         e.connParams.Interceptors,
		OnRTTUpdate:          e.setRTT,
		IsSender:             true,
	}); err != nil {
		return err
	}
	if e.subscriber, err = NewPCTransport(PCTransportParams{
		Configuration:        configuration,
		RetransmitBufferSize: e.connParams.RetransmitBufferSize,
	}); err != nil {
		return err
	}
	e.publisher.SetLogger(e.log)
	e.subscriber.SetLogger(e.log)
	e.log.Debugw("Using ICE servers", "servers", iceServers)

	if subscriberPrimary != nil {
		e.subscriberPrimary = *subscriberPrimary
	}
	e.subscriber.OnRemoteDescriptionSettled(e.createPublisherAnswerAndSend)

	e.publisher.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			// done
			return
		}
		init := candidate.ToJSON()
		e.log.Debugw("local ICE candidate",
			"target", livekit.SignalTarget_PUBLISHER,
			"candidate", init.Candidate,
		)
		if err := e.client.SendICECandidate(init, livekit.SignalTarget_PUBLISHER); err != nil {
			e.log.Errorw("could not send ICE candidates for publisher", err)
		}

	})
	e.subscriber.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			// done
			return
		}
		init := candidate.ToJSON()
		e.log.Debugw("local ICE candidate",
			"target", livekit.SignalTarget_SUBSCRIBER,
			"candidate", init.Candidate,
		)
		if err := e.client.SendICECandidate(init, livekit.SignalTarget_SUBSCRIBER); err != nil {
			e.log.Errorw("could not send ICE candidates for subscriber", err)
		}
	})

	primaryTransport := e.publisher
	if e.subscriberPrimary {
		primaryTransport = e.subscriber
	}
	primaryTransport.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		switch state {
		case webrtc.ICEConnectionStateConnected:
			var fields []interface{}
			if pair, err := primaryTransport.GetSelectedCandidatePair(); err == nil {
				fields = append(fields, "iceCandidatePair", pair)
			}
			e.log.Debugw("ICE connected", fields...)
		case webrtc.ICEConnectionStateDisconnected:
			e.log.Debugw("ICE disconnected")
		case webrtc.ICEConnectionStateFailed:
			e.log.Debugw("ICE failed")
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
		e.hasPublish.Store(true)
		if err := e.client.SendOffer(offer); err != nil {
			e.log.Errorw("could not send offer", err)
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
		if e.closed.Load() {
			e.log.Debugw("ignoring SDP answer after closed")
			return
		}

		if err := e.publisher.SetRemoteDescription(sd); err != nil {
			e.log.Errorw("could not set remote description", err)
		} else {
			e.log.Debugw("successfully set publisher answer")
		}
	}
	e.client.OnTrickle = func(init webrtc.ICECandidateInit, target livekit.SignalTarget) {
		if e.closed.Load() {
			e.log.Debugw("ignoring trickle after closed")
			return
		}

		var err error
		e.log.Debugw("remote ICE candidate",
			"target", target,
			"candidate", init.Candidate,
		)
		if target == livekit.SignalTarget_PUBLISHER {
			err = e.publisher.AddICECandidate(init)
		} else if target == livekit.SignalTarget_SUBSCRIBER {
			err = e.subscriber.AddICECandidate(init)
		}
		if err != nil {
			e.log.Errorw("could not add ICE candidate", err)
		}
	}
	e.client.OnOffer = func(sd webrtc.SessionDescription) {
		if e.closed.Load() {
			e.log.Debugw("ignoring SDP offer after closed")
			return
		}

		e.log.Debugw("received offer for subscriber")
		if err := e.subscriber.SetRemoteDescription(sd); err != nil {
			e.log.Errorw("could not set remote description", err)
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

func waitUntilConnected(d time.Duration, test func() bool) error {
	if test() {
		return nil
	}

	timeout := time.NewTimer(d)
	defer timeout.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timeout.C:
			return ErrConnectionTimeout
		case <-ticker.C:
			if test() {
				return nil
			}
		}
	}
}

func (e *RTCEngine) waitUntilConnected() error {
	return waitUntilConnected(e.JoinTimeout, func() bool {
		if e.IsConnected() {
			e.requiresFullReconnect.Store(false)
			return true
		}
		return false
	})
}

func (e *RTCEngine) ensurePublisherConnected(ensureDataReady bool) error {
	e.pclock.Lock()
	subscriberPrimary := e.subscriberPrimary
	e.pclock.Unlock()
	if !subscriberPrimary {
		return e.waitUntilConnected()
	}

	var negotiated bool
	return waitUntilConnected(e.JoinTimeout, func() bool {
		if publisher, ok := e.Publisher(); ok {
			if publisher.IsConnected() && (!ensureDataReady || e.dataPubChannelReady()) {
				return true
			}
			if !negotiated {
				publisher.Negotiate()
				negotiated = true
			}
		}
		return false
	})
}

func (e *RTCEngine) dataPubChannelReady() bool {
	e.dclock.RLock()
	defer e.dclock.RUnlock()
	return e.reliableDC.ReadyState() == webrtc.DataChannelStateOpen && e.lossyDC.ReadyState() == webrtc.DataChannelStateOpen
}

func (e *RTCEngine) RegisterTrackPublishedListener(cid string, c chan *livekit.TrackPublishedResponse) {
	e.trackPublishedListenersLock.Lock()
	e.trackPublishedListeners[cid] = c
	e.trackPublishedListenersLock.Unlock()
}

func (e *RTCEngine) UnregisterTrackPublishedListener(cid string) {
	e.trackPublishedListenersLock.Lock()
	delete(e.trackPublishedListeners, cid)
	e.trackPublishedListenersLock.Unlock()
}

func (e *RTCEngine) handleLocalTrackPublished(res *livekit.TrackPublishedResponse) {
	e.trackPublishedListenersLock.Lock()
	listener, ok := e.trackPublishedListeners[res.Cid]
	e.trackPublishedListenersLock.Unlock()

	if ok {
		listener <- res
	}
}

func (e *RTCEngine) handleLocalTrackUnpublished(res *livekit.TrackUnpublishedResponse) {
	if e.OnLocalTrackUnpublished != nil {
		e.OnLocalTrackUnpublished(res)
	}
}

func (e *RTCEngine) handleTrackRemoteMuted(request *livekit.MuteTrackRequest) {
	if e.OnTrackRemoteMuted != nil {
		e.OnTrackRemoteMuted(request)
	}
}

func (e *RTCEngine) handleDataPacket(msg webrtc.DataChannelMessage) {
	packet, err := e.readDataPacket(msg)
	if err != nil {
		return
	}
	identity := packet.ParticipantIdentity
	switch msg := packet.Value.(type) {
	case *livekit.DataPacket_User:
		m := msg.User
		//lint:ignore SA1019 backward compatibility
		if ptr := &m.ParticipantIdentity; *ptr == "" {
			*ptr = identity
		}
		//lint:ignore SA1019 backward compatibility
		if ptr := &m.DestinationIdentities; len(*ptr) == 0 {
			*ptr = packet.DestinationIdentities
		}
		if onDataReceived := e.OnDataReceived; onDataReceived != nil {
			onDataReceived(m)
		}
		if e.OnDataPacket != nil {
			if identity == "" {
				//lint:ignore SA1019 backward compatibility
				identity = m.ParticipantIdentity
			}
			e.OnDataPacket(identity, &UserDataPacket{
				Payload: m.Payload,
				Topic:   m.GetTopic(),
			})
		}
	case *livekit.DataPacket_SipDtmf:
		if e.OnDataPacket != nil {
			e.OnDataPacket(identity, msg.SipDtmf)
		}
	case *livekit.DataPacket_Transcription:
		if e.OnTranscription != nil {
			e.OnTranscription(msg.Transcription)
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
		if fullReconnect {
			e.requiresFullReconnect.Store(true)
		}
		return
	}

	go func() {
		defer e.reconnecting.Store(false)
		for reconnectCount := 0; reconnectCount < maxReconnectCount && !e.closed.Load(); reconnectCount++ {
			if e.requiresFullReconnect.Load() {
				fullReconnect = true
			}
			if fullReconnect {
				if reconnectCount == 0 && e.OnRestarting != nil {
					e.OnRestarting()
				}
				e.log.Infow("restarting connection...", "reconnectCount", reconnectCount)
				if err := e.restartConnection(); err != nil {
					e.log.Errorw("restart connection failed", err)
				} else {
					return
				}
			} else {
				if reconnectCount == 0 && e.OnResuming != nil {
					e.OnResuming()
				}
				e.log.Infow("resuming connection...", "reconnectCount", reconnectCount)
				if err := e.resumeConnection(); err != nil {
					e.log.Errorw("resume connection failed", err)
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
			e.OnDisconnected(Failed)
		}
	}()
}

func (e *RTCEngine) resumeConnection() error {
	reconnect, err := e.client.Reconnect(e.url, e.token.Load(), *e.connParams, e.CbGetLocalParticipantSID())
	if err != nil {
		return err
	}

	if reconnect != nil {
		configuration := e.makeRTCConfiguration(reconnect.IceServers, reconnect.ClientConfiguration)
		e.pclock.Lock()
		if err = e.publisher.SetConfiguration(configuration); err != nil {
			logger.Errorw("could not set rtc configuration for publisher", err)
			e.pclock.Unlock()
			return err
		}
		if err = e.subscriber.SetConfiguration(configuration); err != nil {
			logger.Errorw("could not set rtc configuration for subscriber", err)
			e.pclock.Unlock()
			return err
		}
		e.pclock.Unlock()
	}
	e.client.Start()

	// send offer if publisher enabled
	e.pclock.Lock()
	sendOffer := !e.subscriberPrimary || e.hasPublish.Load()
	publisher := e.publisher
	e.pclock.Unlock()
	if sendOffer {
		if err := publisher.createAndSendOffer(&webrtc.OfferOptions{
			ICERestart: true,
		}); err != nil {
			return err
		}
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
		// TODO: special reason for reconnect?
		e.client.SendLeaveWithReason(livekit.DisconnectReason_UNKNOWN_REASON)
	}
	e.client.Close()

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
		e.log.Errorw("could not create answer", err)
		return err
	}
	if err := e.subscriber.pc.SetLocalDescription(answer); err != nil {
		e.log.Errorw("could not set subscriber local description", err)
		return err
	}
	if err := e.client.SendAnswer(answer); err != nil {
		e.log.Errorw("could not send answer for subscriber", err)
		return err
	}
	return nil
}

func (e *RTCEngine) handleLeave(leave *livekit.LeaveRequest) {
	if leave.GetCanReconnect() {
		e.handleDisconnect(true)
	} else {
		reason := leave.GetReason()
		e.log.Infow("server initiated leave",
			"reason", reason,
			"canReconnect", leave.GetCanReconnect(),
		)
		if e.OnDisconnected != nil {
			// TODO: migrate to LeaveRequest.Action
			e.OnDisconnected(GetDisconnectionReason(reason))
		}
	}
}

func (e *RTCEngine) makeRTCConfiguration(iceServers []*livekit.ICEServer, clientConfig *livekit.ClientConfiguration) webrtc.Configuration {
	rtcICEServers := FromProtoIceServers(iceServers)
	configuration := webrtc.Configuration{
		ICEServers:         rtcICEServers,
		ICETransportPolicy: e.connParams.ICETransportPolicy,
	}
	if clientConfig != nil &&
		clientConfig.GetForceRelay() == livekit.ClientConfigSetting_ENABLED {
		configuration.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	return configuration
}
