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
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	protosignalling "github.com/livekit/protocol/signalling"

	"github.com/livekit/server-sdk-go/v2/signalling"
)

// -------------------------------------------

type engineHandler interface {
	OnLocalTrackUnpublished(response *livekit.TrackUnpublishedResponse)
	OnTrackRemoteMuted(request *livekit.MuteTrackRequest)
	OnDisconnected(reason DisconnectionReason)
	OnMediaTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)
	OnParticipantUpdate([]*livekit.ParticipantInfo)
	OnSpeakersChanged([]*livekit.SpeakerInfo)
	OnDataPacket(identity string, dataPacket DataPacket)
	OnConnectionQuality([]*livekit.ConnectionQualityInfo)
	OnRoomUpdate(room *livekit.Room)
	OnRoomMoved(moved *livekit.RoomMovedResponse)
	OnRestarting()
	OnRestarted(
		room *livekit.Room,
		participant *livekit.ParticipantInfo,
		otherParticipants []*livekit.ParticipantInfo,
	)
	OnResuming()
	OnResumed()
	OnTranscription(*livekit.Transcription)
	OnRoomJoined(
		room *livekit.Room,
		participant *livekit.ParticipantInfo,
		otherParticipants []*livekit.ParticipantInfo,
		serverInfo *livekit.ServerInfo,
		sifTrailer []byte,
	)
	OnRpcRequest(callerIdentity, requestId, method, payload string, responseTimeout time.Duration, version uint32)
	OnRpcAck(requestId string)
	OnRpcResponse(requestId string, payload *string, error *RpcError)
	OnStreamHeader(*livekit.DataStream_Header, string)
	OnStreamChunk(*livekit.DataStream_Chunk)
	OnStreamTrailer(*livekit.DataStream_Trailer)
	OnLocalTrackSubscribed(trackSubscribed *livekit.TrackSubscribed)
	OnSubscribedQualityUpdate(subscribedQualityUpdate *livekit.SubscribedQualityUpdate)
	OnMediaSectionsRequirement(mediaSectionsRequirement *livekit.MediaSectionsRequirement)
}

type nullEngineHandler struct{}

func (n *nullEngineHandler) OnLocalTrackUnpublished(response *livekit.TrackUnpublishedResponse)   {}
func (n *nullEngineHandler) OnTrackRemoteMuted(request *livekit.MuteTrackRequest)                 {}
func (n *nullEngineHandler) OnDisconnected(reason DisconnectionReason)                            {}
func (n *nullEngineHandler) OnMediaTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {}
func (n *nullEngineHandler) OnParticipantUpdate([]*livekit.ParticipantInfo)                       {}
func (n *nullEngineHandler) OnSpeakersChanged([]*livekit.SpeakerInfo)                             {}
func (n *nullEngineHandler) OnDataPacket(identity string, dataPacket DataPacket)                  {}
func (n *nullEngineHandler) OnConnectionQuality([]*livekit.ConnectionQualityInfo)                 {}
func (n *nullEngineHandler) OnRoomUpdate(room *livekit.Room)                                      {}
func (n *nullEngineHandler) OnRoomMoved(moved *livekit.RoomMovedResponse)                         {}
func (n *nullEngineHandler) OnRestarting()                                                        {}
func (n *nullEngineHandler) OnRestarted(room *livekit.Room, participant *livekit.ParticipantInfo, otherParticipants []*livekit.ParticipantInfo) {
}
func (n *nullEngineHandler) OnResuming()                                   {}
func (n *nullEngineHandler) OnResumed()                                    {}
func (n *nullEngineHandler) OnTranscription(*livekit.Transcription)        {}
func (n *nullEngineHandler) OnSignalClientConnected(*livekit.JoinResponse) {}
func (n *nullEngineHandler) OnRpcRequest(callerIdentity, requestId, method, payload string, responseTimeout time.Duration, version uint32) {
}
func (n *nullEngineHandler) OnRpcAck(requestId string)                                        {}
func (n *nullEngineHandler) OnRpcResponse(requestId string, payload *string, error *RpcError) {}
func (n *nullEngineHandler) OnStreamHeader(*livekit.DataStream_Header, string)                {}
func (n *nullEngineHandler) OnStreamChunk(*livekit.DataStream_Chunk)                          {}
func (n *nullEngineHandler) OnStreamTrailer(*livekit.DataStream_Trailer)                      {}
func (n *nullEngineHandler) OnLocalTrackSubscribed(trackSubscribed *livekit.TrackSubscribed)  {}
func (n *nullEngineHandler) OnSubscribedQualityUpdate(subscribedQualityUpdate *livekit.SubscribedQualityUpdate) {
}
func (n *nullEngineHandler) OnMediaSectionsRequirement(mediaSectionsRequirement *livekit.MediaSectionsRequirement) {
}

// -------------------------------------------

var (
	_ signalling.SignalTransportHandler = (*RTCEngine)(nil)
	_ signalling.SignalProcessor        = (*RTCEngine)(nil)
)

// -------------------------------------------

const (
	reliableDataChannelName = "_reliable"
	lossyDataChannelName    = "_lossy"

	maxReconnectCount        = 10
	initialReconnectInterval = 300 * time.Millisecond
	maxReconnectInterval     = 60 * time.Second
)

type RTCEngine struct {
	log protoLogger.Logger

	useSinglePeerConnection  bool
	engineHandler            engineHandler
	cbGetLocalParticipantSID func() string

	pclock                sync.Mutex
	publisher             *PCTransport
	pendingPublisherOffer webrtc.SessionDescription
	subscriber            *PCTransport

	signalling      signalling.Signalling
	signalHandler   signalling.SignalHandler
	signalTransport signalling.SignalTransport

	dclock          sync.RWMutex
	reliableDC      *webrtc.DataChannel
	lossyDC         *webrtc.DataChannel
	reliableDCSub   *webrtc.DataChannel
	lossyDCSub      *webrtc.DataChannel
	reliableMsgLock sync.Mutex
	reliableMsgSeq  uint32

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
	connParams *signalling.ConnectParams

	joinTimeout time.Duration

	onClose     []func()
	onCloseLock sync.Mutex
}

func NewRTCEngine(
	useSinglePeerConnection bool,
	engineHandler engineHandler,
	getLocalParticipantSID func() string,
) *RTCEngine {
	e := &RTCEngine{
		log:                      logger,
		useSinglePeerConnection:  useSinglePeerConnection,
		engineHandler:            engineHandler,
		cbGetLocalParticipantSID: getLocalParticipantSID,
		trackPublishedListeners:  make(map[string]chan *livekit.TrackPublishedResponse),
		joinTimeout:              15 * time.Second,
		reliableMsgSeq:           1,
	}
	if !useSinglePeerConnection {
		e.signalling = signalling.NewSignalling(signalling.SignallingParams{
			Logger: e.log,
		})
	} else {
		e.signalling = signalling.NewSignallingJoinRequest(signalling.SignallingJoinRequestParams{
			Logger: e.log,
		})
	}
	e.signalHandler = signalling.NewSignalHandler(signalling.SignalHandlerParams{
		Logger:    e.log,
		Processor: e,
	})
	e.signalTransport = signalling.NewSignalTransportWebSocket(signalling.SignalTransportWebSocketParams{
		Logger:                 e.log,
		Version:                Version,
		Protocol:               PROTOCOL,
		Signalling:             e.signalling,
		SignalTransportHandler: e,
		SignalHandler:          e.signalHandler,
	})

	e.onClose = []func(){}
	return e
}

// SetLogger overrides default logger.
func (e *RTCEngine) SetLogger(l protoLogger.Logger) {
	e.log = l
	e.signalling.SetLogger(l)
	e.signalHandler.SetLogger(l)
	e.signalTransport.SetLogger(l)
	if e.publisher != nil {
		e.publisher.SetLogger(l.WithValues("transport", livekit.SignalTarget_PUBLISHER))
	}
	if e.subscriber != nil {
		e.subscriber.SetLogger(l.WithValues("transport", livekit.SignalTarget_SUBSCRIBER))
	}
}

func (e *RTCEngine) JoinContext(
	ctx context.Context,
	url string,
	token string,
	connectParams *signalling.ConnectParams,
) (bool, error) {
	e.url = url
	e.token.Store(token)
	e.connParams = connectParams

	var (
		publisherOffer webrtc.SessionDescription
		err            error
	)
	if e.signalling.PublishInJoin() {
		e.pclock.Lock()
		e.createPublisherPCLocked(webrtc.Configuration{})

		publisherOffer, err = e.publisher.GetLocalOffer()
		if err != nil {
			e.pclock.Unlock()
			return false, err
		}
		e.pendingPublisherOffer = publisherOffer
		e.pclock.Unlock()
	}

	if err = e.signalTransport.Join(ctx, url, token, *connectParams, nil, publisherOffer); err != nil {
		if verr := e.validate(ctx, url, token, connectParams, ""); verr != nil {
			return false, verr
		}
		return false, ErrCannotConnectSignal
	}

	if err = e.waitUntilConnected(); err != nil {
		return false, err
	}

	e.hasConnected.Store(true)
	return true, nil
}

func (e *RTCEngine) OnClose(onClose func()) {
	e.onCloseLock.Lock()
	e.onClose = append(e.onClose, onClose)
	e.onCloseLock.Unlock()
}

func (e *RTCEngine) Close() {
	if !e.closed.CompareAndSwap(false, true) {
		return
	}

	go func() {
		for e.reconnecting.Load() {
			time.Sleep(50 * time.Millisecond)
		}

		e.onCloseLock.Lock()
		onClose := e.onClose
		e.onClose = []func(){}
		e.onCloseLock.Unlock()

		for _, onCloseHandler := range onClose {
			onCloseHandler()
		}

		if publisher, ok := e.Publisher(); ok {
			_ = publisher.Close()
		}
		if subscriber, ok := e.Subscriber(); ok {
			_ = subscriber.Close()
		}

		e.signalTransport.Close()
	}()
}

func (e *RTCEngine) IsConnected() bool {
	e.pclock.Lock()
	defer e.pclock.Unlock()

	if e.publisher == nil || (!e.useSinglePeerConnection && e.subscriber == nil) {
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
	subscriberPrimary *bool,
) error {
	e.log.Debugw("Using ICE servers", "servers", iceServers)
	configuration := e.makeRTCConfiguration(iceServers, clientConfig)

	// reset reliable message sequence
	e.reliableMsgLock.Lock()
	e.reliableMsgSeq = 1
	e.reliableMsgLock.Unlock()

	e.pclock.Lock()
	defer e.pclock.Unlock()

	if subscriberPrimary != nil {
		e.subscriberPrimary = *subscriberPrimary
	}

	if e.publisher != nil {
		setConfiguration(e.publisher, configuration)
	} else {
		if err := e.createPublisherPCLocked(configuration); err != nil {
			return err
		}
	}

	if e.subscriber != nil {
		setConfiguration(e.subscriber, configuration)
	} else {
		if err := e.createSubscriberPCLocked(configuration); err != nil {
			return err
		}
	}

	return nil
}

func (e *RTCEngine) createPublisherPCLocked(configuration webrtc.Configuration) error {
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
	e.publisher.SetLogger(e.log.WithValues("transport", livekit.SignalTarget_PUBLISHER))

	e.publisher.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			// done
			return
		}
		init := candidate.ToJSON()
		e.log.Debugw(
			"local ICE candidate",
			"transport", livekit.SignalTarget_PUBLISHER,
			"candidate", init.Candidate,
		)
		if err := e.signalTransport.SendMessage(
			e.signalling.SignalICECandidate(
				protosignalling.ToProtoTrickle(init, livekit.SignalTarget_PUBLISHER, false),
			),
		); err != nil {
			e.log.Errorw(
				"could not send ICE candidate", err,
				"transport", livekit.SignalTarget_PUBLISHER,
			)
		}
	})

	e.publisher.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		e.pclock.Lock()
		publisher := e.publisher
		e.pclock.Unlock()
		e.handleICEConnectionStateChange(publisher, livekit.SignalTarget_PUBLISHER, state)
	})

	e.publisher.OnOffer = func(offer webrtc.SessionDescription) {
		e.hasPublish.Store(true)
		if err := e.signalTransport.SendMessage(
			e.signalling.SignalSdpOffer(
				protosignalling.ToProtoSessionDescription(offer, 0, nil),
			),
		); err != nil {
			e.log.Errorw("could not send offer for publisher pc", err)
		}
	}

	trueVal := true
	falseVal := false
	maxRetries := uint16(1)
	e.dclock.Lock()
	e.lossyDC, err = e.publisher.pc.CreateDataChannel(lossyDataChannelName, &webrtc.DataChannelInit{
		Ordered:        &falseVal,
		MaxRetransmits: &maxRetries,
	})
	if err != nil {
		e.dclock.Unlock()
		return err
	}
	e.lossyDC.OnMessage(e.handleDataPacket)

	e.reliableDC, err = e.publisher.pc.CreateDataChannel(reliableDataChannelName, &webrtc.DataChannelInit{
		Ordered: &trueVal,
	})
	if err != nil {
		e.dclock.Unlock()
		return err
	}
	e.reliableDC.OnMessage(e.handleDataPacket)
	e.dclock.Unlock()

	return nil
}

func (e *RTCEngine) createSubscriberPCLocked(configuration webrtc.Configuration) error {
	if e.useSinglePeerConnection {
		return nil
	}

	var err error
	if e.subscriber, err = NewPCTransport(PCTransportParams{
		Configuration:        configuration,
		RetransmitBufferSize: e.connParams.RetransmitBufferSize,
	}); err != nil {
		return err
	}
	e.subscriber.SetLogger(e.log.WithValues("transport", livekit.SignalTarget_SUBSCRIBER))

	e.subscriber.OnRemoteDescriptionSettled(e.createSubscriberPCAnswerAndSend)

	e.subscriber.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			// done
			return
		}
		init := candidate.ToJSON()
		e.log.Debugw(
			"local ICE candidate",
			"transport", livekit.SignalTarget_SUBSCRIBER,
			"candidate", init.Candidate,
		)
		if err := e.signalTransport.SendMessage(
			e.signalling.SignalICECandidate(
				protosignalling.ToProtoTrickle(init, livekit.SignalTarget_SUBSCRIBER, false),
			),
		); err != nil {
			e.log.Errorw(
				"could not send ICE candidate", err,
				"transport", livekit.SignalTarget_SUBSCRIBER,
			)
		}
	})

	e.subscriber.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		e.pclock.Lock()
		subscriber := e.subscriber
		e.pclock.Unlock()
		e.handleICEConnectionStateChange(subscriber, livekit.SignalTarget_SUBSCRIBER, state)
	})

	e.subscriber.pc.OnTrack(func(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		e.engineHandler.OnMediaTrack(remote, receiver)
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

	return nil
}

func (e *RTCEngine) handleICEConnectionStateChange(
	transport *PCTransport,
	signalTarget livekit.SignalTarget,
	state webrtc.ICEConnectionState,
) {
	if transport == nil {
		return
	}

	switch state {
	case webrtc.ICEConnectionStateConnected:
		var fields []any
		if pair, err := transport.GetSelectedCandidatePair(); err == nil {
			fields = append(fields, "transport", signalTarget, "iceCandidatePair", pair)
		}
		e.log.Debugw("ICE connected", fields...)
	case webrtc.ICEConnectionStateDisconnected:
		e.log.Debugw("ICE disconnected", "transport", signalTarget)
	case webrtc.ICEConnectionStateFailed:
		e.log.Debugw("ICE failed", "transport", signalTarget)
		e.handleDisconnect(false)
	}
}

func (e *RTCEngine) closePeerConnections() {
	e.pclock.Lock()
	defer e.pclock.Unlock()

	if e.publisher != nil {
		e.publisher.Close()
		e.publisher = nil
	}

	if e.subscriber != nil {
		e.subscriber.Close()
		e.subscriber = nil
	}
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
	return waitUntilConnected(e.joinTimeout, func() bool {
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
	return waitUntilConnected(e.joinTimeout, func() bool {
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

		if identity == "" {
			//lint:ignore SA1019 backward compatibility
			identity = m.ParticipantIdentity
		}
		e.engineHandler.OnDataPacket(identity, &UserDataPacket{
			Payload: m.Payload,
			Topic:   m.GetTopic(),
		})
	case *livekit.DataPacket_SipDtmf:
		e.engineHandler.OnDataPacket(identity, msg.SipDtmf)
	case *livekit.DataPacket_ChatMessage:
		e.engineHandler.OnDataPacket(identity, msg.ChatMessage)
	case *livekit.DataPacket_Transcription:
		e.engineHandler.OnTranscription(msg.Transcription)
	case *livekit.DataPacket_RpcRequest:
		e.engineHandler.OnRpcRequest(
			packet.ParticipantIdentity,
			msg.RpcRequest.Id,
			msg.RpcRequest.Method,
			msg.RpcRequest.Payload,
			time.Duration(msg.RpcRequest.ResponseTimeoutMs)*time.Millisecond,
			msg.RpcRequest.Version,
		)
	case *livekit.DataPacket_RpcAck:
		e.engineHandler.OnRpcAck(msg.RpcAck.RequestId)
	case *livekit.DataPacket_RpcResponse:
		switch res := msg.RpcResponse.Value.(type) {
		case *livekit.RpcResponse_Payload:
			e.engineHandler.OnRpcResponse(msg.RpcResponse.RequestId, &res.Payload, nil)
		case *livekit.RpcResponse_Error:
			e.engineHandler.OnRpcResponse(msg.RpcResponse.RequestId, nil, fromProto(res.Error))
		}
	case *livekit.DataPacket_StreamHeader:
		e.engineHandler.OnStreamHeader(msg.StreamHeader, identity)
	case *livekit.DataPacket_StreamChunk:
		e.engineHandler.OnStreamChunk(msg.StreamChunk)
	case *livekit.DataPacket_StreamTrailer:
		e.engineHandler.OnStreamTrailer(msg.StreamTrailer)
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
				if reconnectCount == 0 {
					e.engineHandler.OnRestarting()
				}
				e.log.Infow("restarting connection...", "reconnectCount", reconnectCount)
				if err := e.restartConnection(); err != nil {
					e.log.Errorw("restart connection failed", err)
				} else {
					return
				}
			} else {
				if reconnectCount == 0 {
					e.engineHandler.OnResuming()
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

		e.engineHandler.OnDisconnected(Failed)
	}()
}

func (e *RTCEngine) resumeConnection() error {
	err := e.signalTransport.Reconnect(
		e.url,
		e.token.Load(),
		*e.connParams,
		e.cbGetLocalParticipantSID(),
	)
	if err != nil {
		if verr := e.validate(
			context.TODO(),
			e.url,
			e.token.Load(),
			e.connParams,
			e.cbGetLocalParticipantSID(),
		); verr != nil {
			return verr
		}
		return ErrCannotConnectSignal
	}

	e.signalTransport.Start()

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

	e.engineHandler.OnResumed()
	return nil
}

func (e *RTCEngine) restartConnection() error {
	if e.signalTransport.IsStarted() {
		// TODO: special reason for reconnect?
		e.SendLeaveWithReason(livekit.DisconnectReason_UNKNOWN_REASON)
	}
	e.signalTransport.Close()

	e.closePeerConnections()

	_, err := e.JoinContext(context.TODO(), e.url, e.token.Load(), e.connParams)
	return err
}

func (e *RTCEngine) createSubscriberPCAnswerAndSend() error {
	answer, err := e.subscriber.pc.CreateAnswer(nil)
	if err != nil {
		e.log.Errorw("could not create answer", err)
		return err
	}
	if err := e.subscriber.pc.SetLocalDescription(answer); err != nil {
		e.log.Errorw("could not set subscriber local description", err)
		return err
	}
	e.log.Debugw("sending answer for subscriber", "answer", answer)
	if err := e.signalTransport.SendMessage(
		e.signalling.SignalSdpAnswer(
			protosignalling.ToProtoSessionDescription(answer, 0, nil),
		),
	); err != nil {
		e.log.Errorw("could not send answer for subscriber pc", err)
	}
	return nil
}

func (e *RTCEngine) makeRTCConfiguration(iceServers []*livekit.ICEServer, clientConfig *livekit.ClientConfiguration) webrtc.Configuration {
	rtcICEServers := protosignalling.FromProtoIceServers(iceServers)
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

func (e *RTCEngine) publishDataPacket(pck *livekit.DataPacket, kind livekit.DataPacket_Kind) error {
	err := e.ensurePublisherConnected(true)
	if err != nil {
		e.log.Errorw("could not ensure publisher connected", err)
		return err
	}

	dc := e.GetDataChannel(kind)
	if dc == nil {
		e.log.Errorw("could not get data channel", nil, "kind", kind)
		return errors.New("datachannel not found")
	}

	if kind == livekit.DataPacket_RELIABLE {
		e.reliableMsgLock.Lock()
		defer e.reliableMsgLock.Unlock()

		pck.Sequence = e.reliableMsgSeq
		e.reliableMsgSeq++
	}

	data, err := proto.Marshal(pck)
	if err != nil {
		e.log.Errorw("could not marshal data packet", err)
		return err
	}

	dc.Send(data)
	return nil
}

func (e *RTCEngine) publishDataPacketReliable(pck *livekit.DataPacket) error {
	return e.publishDataPacket(pck, livekit.DataPacket_RELIABLE)
}

//lint:ignore U1000 Ignore unused function
func (e *RTCEngine) publishDataPacketLossy(pck *livekit.DataPacket) error {
	return e.publishDataPacket(pck, livekit.DataPacket_LOSSY)
}

// TODO: adjust RPC methods to return error on publishDataPacket failure
func (e *RTCEngine) publishRpcResponse(destinationIdentity, requestId string, payload *string, err *RpcError) error {
	packet := &livekit.DataPacket{
		DestinationIdentities: []string{destinationIdentity},
		Value: &livekit.DataPacket_RpcResponse{
			RpcResponse: &livekit.RpcResponse{
				RequestId: requestId,
			},
		},
	}

	if err != nil {
		packet.Value.(*livekit.DataPacket_RpcResponse).RpcResponse.Value = &livekit.RpcResponse_Error{
			Error: err.toProto(),
		}
	} else {
		if payload == nil {
			emptyStr := ""
			payload = &emptyStr
		}

		packet.Value.(*livekit.DataPacket_RpcResponse).RpcResponse.Value = &livekit.RpcResponse_Payload{
			Payload: *payload,
		}
	}

	publishErr := e.publishDataPacketReliable(packet)
	if publishErr != nil {
		e.log.Errorw("could not publish rpc response", publishErr)
	}
	return publishErr
}

func (e *RTCEngine) publishRpcAck(destinationIdentity, requestId string) error {
	packet := &livekit.DataPacket{
		DestinationIdentities: []string{destinationIdentity},
		Value: &livekit.DataPacket_RpcAck{
			RpcAck: &livekit.RpcAck{
				RequestId: requestId,
			},
		},
	}

	publishErr := e.publishDataPacketReliable(packet)
	if publishErr != nil {
		e.log.Errorw("could not publish rpc ack", publishErr)
	}
	return publishErr
}

func (e *RTCEngine) publishRpcRequest(destinationIdentity, requestId, method, payload string, responseTimeout time.Duration) error {
	packet := &livekit.DataPacket{
		DestinationIdentities: []string{destinationIdentity},
		Value: &livekit.DataPacket_RpcRequest{
			RpcRequest: &livekit.RpcRequest{
				Id:                requestId,
				Method:            method,
				Payload:           payload,
				ResponseTimeoutMs: uint32(responseTimeout.Milliseconds()),
				Version:           1,
			},
		},
	}

	publishErr := e.publishDataPacketReliable(packet)
	if publishErr != nil {
		e.log.Errorw("could not publish rpc request", publishErr)
	}
	return publishErr
}

func (e *RTCEngine) publishStreamHeader(header *livekit.DataStream_Header, destinationIdentities []string) error {
	packet := &livekit.DataPacket{
		DestinationIdentities: destinationIdentities,
		Value: &livekit.DataPacket_StreamHeader{
			StreamHeader: header,
		},
	}

	publishErr := e.publishDataPacketReliable(packet)
	if publishErr != nil {
		e.log.Errorw("could not publish stream header", publishErr)
	}
	return publishErr
}

func (e *RTCEngine) publishStreamChunk(chunk *livekit.DataStream_Chunk, destinationIdentities []string) error {
	packet := &livekit.DataPacket{
		DestinationIdentities: destinationIdentities,
		Value: &livekit.DataPacket_StreamChunk{
			StreamChunk: chunk,
		},
	}

	publishErr := e.publishDataPacketReliable(packet)
	if publishErr != nil {
		e.log.Errorw("could not publish stream chunk", publishErr)
	}
	return publishErr
}

func (e *RTCEngine) publishStreamTrailer(streamId string, destinationIdentities []string) error {
	packet := &livekit.DataPacket{
		DestinationIdentities: destinationIdentities,
		Value: &livekit.DataPacket_StreamTrailer{
			StreamTrailer: &livekit.DataStream_Trailer{
				StreamId: streamId,
			},
		},
	}

	publishErr := e.publishDataPacketReliable(packet)
	if publishErr != nil {
		e.log.Errorw("could not publish stream trailer", publishErr)
	}
	return publishErr
}

func (e *RTCEngine) isBufferStatusLow(kind livekit.DataPacket_Kind) bool {
	dc := e.GetDataChannel(kind)
	if dc != nil {
		return dc.BufferedAmount() <= dc.BufferedAmountLowThreshold()
	}
	return false
}

func (e *RTCEngine) waitForBufferStatusLow(kind livekit.DataPacket_Kind) {
	for !e.isBufferStatusLow(kind) {
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *RTCEngine) SendAddTrack(addTrack *livekit.AddTrackRequest) error {
	return e.signalTransport.SendMessage(e.signalling.SignalAddTrack(addTrack))
}

func (e *RTCEngine) SendSubscriptionPermission(subscriptionPermission *livekit.SubscriptionPermission) error {
	return e.signalTransport.SendMessage(e.signalling.SignalSubscriptionPermission(subscriptionPermission))
}

func (e *RTCEngine) SendUpdateTrackSettings(settings *livekit.UpdateTrackSettings) error {
	return e.signalTransport.SendMessage(e.signalling.SignalUpdateTrackSettings(settings))
}

func (e *RTCEngine) SendUpdateParticipantMetadata(metadata *livekit.UpdateParticipantMetadata) error {
	return e.signalTransport.SendMessage(e.signalling.SignalUpdateParticipantMetadata(metadata))
}

func (e *RTCEngine) SendMuteTrack(sid string, muted bool) error {
	return e.signalTransport.SendMessage(
		e.signalling.SignalMuteTrack(
			&livekit.MuteTrackRequest{
				Sid:   sid,
				Muted: muted,
			},
		),
	)
}

func (e *RTCEngine) SendUpdateSubscription(updateSubscription *livekit.UpdateSubscription) error {
	return e.signalTransport.SendMessage(e.signalling.SignalUpdateSubscription(updateSubscription))
}

func (e *RTCEngine) SendSyncState(syncState *livekit.SyncState) error {
	return e.signalTransport.SendMessage(e.signalling.SignalSyncState(syncState))
}

func (e *RTCEngine) SendLeaveWithReason(reason livekit.DisconnectReason) error {
	return e.signalTransport.SendMessage(
		e.signalling.SignalLeaveRequest(
			&livekit.LeaveRequest{
				Reason: reason,
			},
		),
	)
}

func (e *RTCEngine) Simulate(scenario SimulateScenario) {
	switch scenario {
	case SimulateSignalReconnect:
		e.signalTransport.Close()

	case SimulateForceTCP:
		// pion does not support active tcp candidate, skip

	case SimulateForceTLS:
		e.signalTransport.SendMessage(
			e.signalling.SignalSimulateScenario(
				&livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_SwitchCandidateProtocol{
						SwitchCandidateProtocol: livekit.CandidateProtocol_TLS,
					},
				},
			),
		)
		e.OnLeave(&livekit.LeaveRequest{
			Action: livekit.LeaveRequest_RECONNECT,
			Reason: livekit.DisconnectReason_CLIENT_INITIATED,
		})

	case SimulateSpeakerUpdate:
		e.signalTransport.SendMessage(
			e.signalling.SignalSimulateScenario(
				&livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_SpeakerUpdate{
						SpeakerUpdate: SimulateSpeakerUpdateInterval,
					},
				},
			),
		)

	case SimulateMigration:
		e.signalTransport.SendMessage(
			e.signalling.SignalSimulateScenario(
				&livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_Migration{
						Migration: true,
					},
				},
			),
		)

	case SimulateServerLeave:
		e.signalTransport.SendMessage(
			e.signalling.SignalSimulateScenario(
				&livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_ServerLeave{
						ServerLeave: true,
					},
				},
			),
		)

	case SimulateNodeFailure:
		e.signalTransport.SendMessage(
			e.signalling.SignalSimulateScenario(
				&livekit.SimulateScenario{
					Scenario: &livekit.SimulateScenario_NodeFailure{
						NodeFailure: true,
					},
				},
			),
		)
	}
}

func (e *RTCEngine) validate(
	ctx context.Context,
	urlPrefix string,
	token string,
	connectParams *signalling.ConnectParams,
	participantSID string,
) error {
	req, err := e.signalling.HTTPRequestForValidate(
		ctx,
		Version,
		PROTOCOL,
		urlPrefix,
		token,
		connectParams,
		participantSID,
	)
	if err != nil {
		return err
	}

	hresp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.log.Errorw("error getting validation", err, "httpResponse", hresp)
		return signalling.ErrCannotDialSignal
	}
	defer hresp.Body.Close()

	if hresp.StatusCode == http.StatusOK {
		// no specific errors to return if validate succeeds
		e.log.Infow("validate succeeded")
		return nil
	}

	var errString string
	switch hresp.StatusCode {
	case http.StatusUnauthorized:
		errString = "unauthorized: "
	case http.StatusNotFound:
		errString = "not found"
	case http.StatusServiceUnavailable:
		errString = "unavailable: "
	}
	if hresp.StatusCode != http.StatusNotFound {
		body, err := io.ReadAll(hresp.Body)
		if err == nil {
			errString += e.signalling.DecodeErrorResponse(body)
		}
		e.log.Errorw("validation error", errors.New(errString), "httpResponse", hresp)
	} else {
		e.log.Errorw("validation error", errors.New(errString))
	}
	return errors.New(errString)
}

// signalling.SignalTransportHandler implementation
func (e *RTCEngine) OnTransportClose() {
	e.handleDisconnect(false)
}

// signalling.SignalProcessor implementation
func (e *RTCEngine) OnJoinResponse(res *livekit.JoinResponse) error {
	isRestarting := false
	if e.reconnecting.Load() && e.requiresFullReconnect.Load() {
		isRestarting = true
	}

	err := e.configure(res.IceServers, res.ClientConfiguration, proto.Bool(res.SubscriberPrimary))
	if err != nil {
		e.log.Warnw("could not configure", err)
		return err
	}

	e.engineHandler.OnRoomJoined(
		res.Room,
		res.Participant,
		res.OtherParticipants,
		res.ServerInfo,
		res.SifTrailer,
	)

	e.signalTransport.Start()

	if e.signalling.PublishInJoin() {
		if publisher, ok := e.Publisher(); ok {
			e.pclock.Lock()
			pendingPublisherOffer := e.pendingPublisherOffer
			e.pendingPublisherOffer = webrtc.SessionDescription{}
			e.pclock.Unlock()

			if pendingPublisherOffer.SDP != "" {
				publisher.SetLocalOffer(pendingPublisherOffer)
			}
		}
	} else {
		// send offer
		if !res.SubscriberPrimary || res.FastPublish {
			if publisher, ok := e.Publisher(); ok {
				publisher.Negotiate()
			} else {
				e.log.Warnw("no publisher peer connection", ErrNoPeerConnection)
			}
		}
	}

	if isRestarting {
		e.engineHandler.OnRestarted(res.Room, res.Participant, res.OtherParticipants)
	}
	return nil
}

func (e *RTCEngine) OnReconnectResponse(res *livekit.ReconnectResponse) error {
	configuration := e.makeRTCConfiguration(res.IceServers, res.ClientConfiguration)

	e.pclock.Lock()
	defer e.pclock.Unlock()

	if e.publisher != nil {
		if err := e.publisher.SetConfiguration(configuration); err != nil {
			e.log.Errorw("could not set rtc configuration for publisher", err)
			return err
		}
	}

	if e.subscriber != nil {
		if err := e.subscriber.SetConfiguration(configuration); err != nil {
			e.log.Errorw("could not set rtc configuration for subscriber", err)
			return err
		}
	}

	return nil
}

func (e *RTCEngine) OnAnswer(sd webrtc.SessionDescription, answerId uint32, _midToTrackID map[string]string) {
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

func (e *RTCEngine) OnOffer(sd webrtc.SessionDescription, offerId uint32, _midToTrackID map[string]string) {
	if e.closed.Load() {
		e.log.Debugw("ignoring SDP offer after closed")
		return
	}

	e.log.Debugw("received offer for subscriber", "offer", sd, "offerId", offerId)
	if err := e.subscriber.SetRemoteDescription(sd); err != nil {
		e.log.Errorw("could not set remote description", err)
		return
	}
}

func (e *RTCEngine) OnTrickle(init webrtc.ICECandidateInit, target livekit.SignalTarget) {
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

func (e *RTCEngine) OnParticipantUpdate(info []*livekit.ParticipantInfo) {
	e.engineHandler.OnParticipantUpdate(info)
}

func (e *RTCEngine) OnLocalTrackPublished(res *livekit.TrackPublishedResponse) {
	e.trackPublishedListenersLock.Lock()
	listener, ok := e.trackPublishedListeners[res.Cid]
	e.trackPublishedListenersLock.Unlock()

	if ok {
		listener <- res
	}
}

func (e *RTCEngine) OnLocalTrackUnpublished(res *livekit.TrackUnpublishedResponse) {
	e.engineHandler.OnLocalTrackUnpublished(res)
}

func (e *RTCEngine) OnSpeakersChanged(si []*livekit.SpeakerInfo) {
	e.engineHandler.OnSpeakersChanged(si)
}

func (e *RTCEngine) OnConnectionQuality(cqi []*livekit.ConnectionQualityInfo) {
	e.engineHandler.OnConnectionQuality(cqi)
}

func (e *RTCEngine) OnRoomUpdate(room *livekit.Room) {
	e.engineHandler.OnRoomUpdate(room)
}

func (e *RTCEngine) OnRoomMoved(moved *livekit.RoomMovedResponse) {
	e.engineHandler.OnRoomMoved(moved)
}

func (e *RTCEngine) OnTrackRemoteMuted(request *livekit.MuteTrackRequest) {
	e.engineHandler.OnTrackRemoteMuted(request)
}

func (e *RTCEngine) OnTokenRefresh(refreshToken string) {
	e.token.Store(refreshToken)
}

func (e *RTCEngine) OnLeave(leave *livekit.LeaveRequest) {
	e.log.Debugw("received leave request", "action", leave.GetAction())
	switch leave.GetAction() {
	case livekit.LeaveRequest_DISCONNECT:
		e.Close()
		reason := leave.GetReason()
		e.log.Infow("server initiated leave", "reason", reason)
		e.engineHandler.OnDisconnected(GetDisconnectionReason(reason))

	case livekit.LeaveRequest_RECONNECT:
		e.handleDisconnect(true)

	case livekit.LeaveRequest_RESUME:
		e.handleDisconnect(false)

	default:
	}
}

func (e *RTCEngine) OnLocalTrackSubscribed(trackSubscribed *livekit.TrackSubscribed) {
	e.engineHandler.OnLocalTrackSubscribed(trackSubscribed)
}

func (e *RTCEngine) OnSubscribedQualityUpdate(subscribedQualityUpdate *livekit.SubscribedQualityUpdate) {
	e.engineHandler.OnSubscribedQualityUpdate(subscribedQualityUpdate)
}

func (e *RTCEngine) OnMediaSectionsRequirement(mediaSectionsRequirement *livekit.MediaSectionsRequirement) {
	e.engineHandler.OnMediaSectionsRequirement(mediaSectionsRequirement)
}

// ------------------------------------

func setConfiguration(pcTransport *PCTransport, configuration webrtc.Configuration) {
	if pcTransport != nil {
		pcTransport.SetConfiguration(configuration)
	}
}
