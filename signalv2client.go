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
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"runtime"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
)

type Signalv2Client struct {
	log protoLogger.Logger
	/* RAJA-REMOVE
	conn            atomic.Pointer[websocket.Conn]
	pendingResponse *livekit.SignalResponse
	readerClosedCh  chan struct{}
	*/
	lock             sync.RWMutex
	isStarted        bool
	messageId        uint32
	httpSignalling   *httpSignalling
	activeSignalling signalTransport

	/* RAJA-TODO
	OnClose                 func()
	OnAnswer                func(sd webrtc.SessionDescription)
	OnOffer                 func(sd webrtc.SessionDescription)
	OnTrickle               func(init webrtc.ICECandidateInit, target livekit.SignalTarget)
	OnParticipantUpdate     func([]*livekit.ParticipantInfo)
	OnLocalTrackPublished   func(response *livekit.TrackPublishedResponse)
	OnSpeakersChanged       func([]*livekit.SpeakerInfo)
	OnConnectionQuality     func([]*livekit.ConnectionQualityInfo)
	OnRoomUpdate            func(room *livekit.Room)
	OnRoomMoved             func(moved *livekit.RoomMovedResponse)
	OnTrackRemoteMuted      func(request *livekit.MuteTrackRequest)
	OnLocalTrackUnpublished func(response *livekit.TrackUnpublishedResponse)
	OnTokenRefresh          func(refreshToken string)
	OnLeave                 func(*livekit.LeaveRequest)
	OnLocalTrackSubscribed  func(trackSubscribed *livekit.TrackSubscribed)
	*/
}

func NewSignalv2Client() *Signalv2Client {
	c := &Signalv2Client{
		log:            logger,
		messageId:      uint32(rand.Intn(1<<8)) + 1, // non-zero starting message id
		httpSignalling: newHttpSignalling(),
	}

	c.activeSignalling = c.httpSignalling
	return c
}

// SetLogger overrides default logger.
func (c *Signalv2Client) SetLogger(l protoLogger.Logger) {
	c.log = l
}

func (c *Signalv2Client) Start() {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.isStarted = true
}

func (c *Signalv2Client) IsStarted() bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.isStarted
}

func (c *Signalv2Client) JoinContext(
	ctx context.Context,
	urlPrefix string,
	token string,
	params SignalClientConnectParams,
) error {
	c.httpSignalling.SetUrlPrefix(urlPrefix)

	return c.connectv2Context(ctx, token, params, "")
}

/* RAJA-TODO
// Reconnect starts a new WebSocket connection to the server, passing in reconnect=1
// when successful, it'll return a ReconnectResponse; older versions of the server will not send back a ReconnectResponse
func (c *Signalv2Client) Reconnect(
	urlPrefix string,
	token string,
	params SignalClientConnectParams,
	participantSID string,
) (*livekit.ReconnectResponse, error) {
	params.Reconnect = true
	res, err := c.connect(urlPrefix, token, params, participantSID)
	if err != nil {
		return nil, err
	}

	c.pendingResponse = nil
	c.log.Debugw("reconnect received response", "response", res.String())
	if res != nil {
		switch msg := res.Message.(type) {
		case *livekit.SignalResponse_Reconnect:
			return msg.Reconnect, nil
		case *livekit.SignalResponse_Leave:
			if c.OnLeave != nil {
				c.OnLeave(msg.Leave)
			}
			return nil, fmt.Errorf("reconnect received left, reason: %s", msg.Leave.GetReason())
		}
		c.pendingResponse = res
	}
	return nil, nil
}
*/

func (c *Signalv2Client) connectv2Context(
	ctx context.Context,
	token string,
	params SignalClientConnectParams,
	participantSID string,
) error {
	clientInfo := &livekit.ClientInfo{
		Protocol: PROTOCOL,
		Version:  Version,
		Os:       runtime.GOOS,
		Sdk:      livekit.ClientInfo_GO,
	}

	connectionSettings := &livekit.ConnectionSettings{}
	if params.AutoSubscribe {
		connectionSettings.AutoSubscribe = true
	}

	connectRequest := &livekit.ConnectRequest{
		ClientInfo:         clientInfo,
		ConnectionSettings: connectionSettings,
	}

	signalv2ClientMessage := &livekit.Signalv2ClientMessage{
		Message: &livekit.Signalv2ClientMessage_ConnectRequest{
			ConnectRequest: connectRequest,
		},
	}

	// RAJA-TODO: need locking to ensure message sequence and delivery order
	_, err := c.activeSignalling.SendMessage(c.makeSignalv2ClientEnvelope(signalv2ClientMessage))
	return err
}

func (c *Signalv2Client) Close() {
}

/* RAJA-TODO
func (c *Signalv2Client) SendICECandidate(candidate webrtc.ICECandidateInit, target livekit.SignalTarget) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Trickle{
			Trickle: ToProtoTrickle(candidate, target),
		},
	})
}

func (c *Signalv2Client) SendOffer(sd webrtc.SessionDescription) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Offer{
			Offer: ToProtoSessionDescription(sd),
		},
	})
}

func (c *Signalv2Client) SendAnswer(sd webrtc.SessionDescription) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Answer{
			Answer: ToProtoSessionDescription(sd),
		},
	})
}

func (c *Signalv2Client) SendMuteTrack(sid string, muted bool) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Mute{
			Mute: &livekit.MuteTrackRequest{
				Sid:   sid,
				Muted: muted,
			},
		},
	})
}

func (c *Signalv2Client) SendSyncState(state *livekit.SyncState) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_SyncState{
			SyncState: state,
		},
	})
}

func (c *Signalv2Client) SendLeave() error {
	return c.SendLeaveWithReason(livekit.DisconnectReason_UNKNOWN_REASON)
}

func (c *Signalv2Client) SendLeaveWithReason(reason livekit.DisconnectReason) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Leave{
			Leave: &livekit.LeaveRequest{
				Reason: reason,
			},
		},
	})
}

func (c *Signalv2Client) SendRequest(req *livekit.SignalRequest) error {
	conn := c.websocketConn()
	if conn == nil {
		return errors.New("client is not connected")
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	return conn.WriteMessage(websocket.BinaryMessage, payload)
}

func (c *Signalv2Client) SendUpdateTrackSettings(settings *livekit.UpdateTrackSettings) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_TrackSetting{
			TrackSetting: settings,
		},
	})
}

func (c *Signalv2Client) SendUpdateParticipantMetadata(metadata *livekit.UpdateParticipantMetadata) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_UpdateMetadata{
			UpdateMetadata: metadata,
		},
	})
}
*/

/* RAJA-TODO
func (c *Signalv2Client) readResponse() (*livekit.SignalResponse, error) {
	conn := c.websocketConn()
	if conn == nil {
		return nil, errors.New("cannot read response before join")
	}
	// handle special messages and pass on the rest
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	msg := &livekit.SignalResponse{}
	switch messageType {
	case websocket.BinaryMessage:
		// protobuf encoded
		err := proto.Unmarshal(payload, msg)
		return msg, err
	case websocket.TextMessage:
		// json encoded
		err := protojson.Unmarshal(payload, msg)
		return msg, err
	default:
		return nil, nil
	}
}

func (c *Signalv2Client) handleResponse(res *livekit.SignalResponse) {
	switch msg := res.Message.(type) {
	case *livekit.SignalResponse_Answer:
		if c.OnAnswer != nil {
			c.OnAnswer(FromProtoSessionDescription(msg.Answer))
		}
	case *livekit.SignalResponse_Offer:
		if c.OnOffer != nil {
			c.OnOffer(FromProtoSessionDescription(msg.Offer))
		}
	case *livekit.SignalResponse_Trickle:
		if c.OnTrickle != nil {
			c.OnTrickle(FromProtoTrickle(msg.Trickle), msg.Trickle.Target)
		}
	case *livekit.SignalResponse_Update:
		if c.OnParticipantUpdate != nil {
			c.OnParticipantUpdate(msg.Update.Participants)
		}
	case *livekit.SignalResponse_SpeakersChanged:
		if c.OnSpeakersChanged != nil {
			c.OnSpeakersChanged(msg.SpeakersChanged.Speakers)
		}
	case *livekit.SignalResponse_TrackPublished:
		if c.OnLocalTrackPublished != nil {
			c.OnLocalTrackPublished(msg.TrackPublished)
		}
	case *livekit.SignalResponse_Mute:
		if c.OnTrackRemoteMuted != nil {
			c.OnTrackRemoteMuted(msg.Mute)
		}
	case *livekit.SignalResponse_ConnectionQuality:
		if c.OnConnectionQuality != nil {
			c.OnConnectionQuality(msg.ConnectionQuality.Updates)
		}
	case *livekit.SignalResponse_RoomUpdate:
		if c.OnRoomUpdate != nil {
			c.OnRoomUpdate(msg.RoomUpdate.Room)
		}
	case *livekit.SignalResponse_RoomMoved:
		if c.OnTokenRefresh != nil {
			c.OnTokenRefresh(msg.RoomMoved.Token)
		}

		if c.OnRoomMoved != nil {
			c.OnRoomMoved(msg.RoomMoved)
		}

	case *livekit.SignalResponse_Leave:
		if c.OnLeave != nil {
			c.OnLeave(msg.Leave)
		}
	case *livekit.SignalResponse_RefreshToken:
		if c.OnTokenRefresh != nil {
			c.OnTokenRefresh(msg.RefreshToken)
		}
	case *livekit.SignalResponse_TrackUnpublished:
		if c.OnLocalTrackUnpublished != nil {
			c.OnLocalTrackUnpublished(msg.TrackUnpublished)
		}
	case *livekit.SignalResponse_TrackSubscribed:
		if c.OnLocalTrackSubscribed != nil {
			c.OnLocalTrackSubscribed(msg.TrackSubscribed)
		}
	}
}

func (c *Signalv2Client) readWorker(readerClosedCh chan struct{}) {
	defer func() {
		c.isStarted.Store(false)
		c.conn.Store(nil)
		close(readerClosedCh)

		if c.OnClose != nil {
			c.OnClose()
		}
	}()
	if pending := c.pendingResponse; pending != nil {
		c.handleResponse(pending)
		c.pendingResponse = nil
	}
	for {
		res, err := c.readResponse()
		if err != nil {
			if !isIgnoredWebsocketError(err) {
				c.log.Infow("error while reading from signal client", "err", err)
			}
			return
		}
		c.handleResponse(res)
	}
}
*/

// RAJA-TODO
// 1. Cache of unacked messages
// 2. Client envelope builder to allow adding messages to the envelope and send envelope whenever
func (c *Signalv2Client) makeSignalv2ClientEnvelope(msg *livekit.Signalv2ClientMessage) *livekit.Signalv2ClientEnvelope {
	msg.MessageId = c.messageId
	c.messageId++

	return &livekit.Signalv2ClientEnvelope{
		ClientMessages: []*livekit.Signalv2ClientMessage{msg},
	}
}

// ----------------------------------------------

type signalTransport interface {
	SendMessage(msg *livekit.Signalv2ClientEnvelope) (*livekit.Signalv2ServerEnvelope, error)
}

// -----------------------------------------------

type httpSignalling struct {
	urlPrefix string
}

func newHttpSignalling() *httpSignalling {
	return &httpSignalling{}
}

func (h *httpSignalling) SetUrlPrefix(urlPrefix string) {
	h.urlPrefix = ToHttpURL(urlPrefix)
}

func (h *httpSignalling) SendMessage(msg *livekit.Signalv2ClientEnvelope) (*livekit.Signalv2ServerEnvelope, error) {
	if len(h.urlPrefix) == 0 {
		return nil, ErrURLNotProvided
	}

	protoMsg, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(h.urlPrefix + "/rtc/v2")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", u.String(), bytes.NewBuffer(protoMsg))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-protobuf")

	httpClient := &http.Client{}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	fmt.Println("Response Status:", resp.Status)
	fmt.Println("Response Body:", string(body))

	respMsg := &livekit.Signalv2ServerEnvelope{}
	if err := proto.Unmarshal(body, respMsg); err != nil {
		return nil, err
	}

	return respMsg, nil
}

// -----------------------------------------------
