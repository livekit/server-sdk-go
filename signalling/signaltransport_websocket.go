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

package signalling

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var _ SignalTransport = (*signalTransportWebSocket)(nil)

type SignalTransportWebSocketParams struct {
	Logger                 logger.Logger
	Version                string
	Protocol               int
	SignalTransportHandler SignalTransportHandler
	SignalHandler          SignalHandler
}

type signalTransportWebSocket struct {
	signalTransportUnimplemented

	params SignalTransportWebSocketParams

	conn      atomic.Pointer[websocket.Conn]
	lock      sync.Mutex
	isStarted atomic.Bool
	// RAJA-TODO pendingResponse *livekit.SignalResponse
	readerClosedCh chan struct{}
}

func NewSignalTransportWebSocket(params SignalTransportWebSocketParams) SignalTransport {
	return &signalTransportWebSocket{
		params: params,
	}
}

func (s *signalTransportWebSocket) SetLogger(l logger.Logger) {
	s.params.Logger = l
}

func (s *signalTransportWebSocket) Start() {
	if s.isStarted.Swap(true) {
		return
	}
	s.readerClosedCh = make(chan struct{})
	go s.readWorker(s.readerClosedCh)
}

func (s *signalTransportWebSocket) IsStarted() bool {
	return s.isStarted.Load()
}

func (s *signalTransportWebSocket) Close() {
	isStarted := s.IsStarted()
	readerClosedCh := s.readerClosedCh
	conn := s.websocketConn()
	if conn != nil {
		_ = conn.Close()
	}
	if isStarted && readerClosedCh != nil {
		<-readerClosedCh
	}
}

func (s *signalTransportWebSocket) Join(
	ctx context.Context,
	urlPrefix string,
	token string,
	connectParams ConnectParams,
) error {
	return s.connect(ctx, urlPrefix, token, connectParams, "")
}

func (s *signalTransportWebSocket) SendMessage(msg proto.Message) error {
	conn := s.websocketConn()
	if conn == nil {
		return errors.New("signal transport is not connected")
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	return conn.WriteMessage(websocket.BinaryMessage, payload)
}

func (s *signalTransportWebSocket) connect(
	ctx context.Context,
	urlPrefix string,
	token string,
	params ConnectParams,
	participantSID string,
) error {
	if urlPrefix == "" {
		return ErrURLNotProvided
	}
	urlPrefix = ToWebsocketURL(urlPrefix)
	urlSuffix := fmt.Sprintf("/rtc?protocol=%d&version=%s", s.params.Protocol, s.params.Version)

	if params.AutoSubscribe {
		urlSuffix += "&auto_subscribe=1"
	} else {
		urlSuffix += "&auto_subscribe=0"
	}
	if params.Reconnect {
		urlSuffix += "&reconnect=1"
		if participantSID != "" {
			urlSuffix += fmt.Sprintf("&sid=%s", participantSID)
		}
	}
	if len(params.Attributes) != 0 {
		data, err := json.Marshal(params.Attributes)
		if err != nil {
			return ErrInvalidParameter
		}
		str := base64.URLEncoding.EncodeToString(data)
		urlSuffix += "&attributes=" + str
	}
	urlSuffix += getStatsParamString()

	u, err := url.Parse(urlPrefix + urlSuffix)
	if err != nil {
		return err
	}

	header := NewHeaderWithToken(token)
	startedAt := time.Now()
	conn, hresp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		fields := []interface{}{
			"duration", time.Since(startedAt),
		}
		if hresp != nil {
			body, _ := io.ReadAll(hresp.Body)
			fields = append(fields, "status", hresp.StatusCode, "response", string(body))
		}
		s.params.Logger.Errorw("error establishing signal connection", err, fields...)

		if strings.HasSuffix(err.Error(), ":53: server misbehaving") {
			// DNS issue, abort
			return ErrCannotDialSignal
		}
		// use validate endpoint to get the actual error
		validateSuffix := strings.Replace(urlSuffix, "/rtc", "/rtc/validate", 1)

		validateReq, err1 := http.NewRequestWithContext(ctx, http.MethodGet, ToHttpURL(urlPrefix)+validateSuffix, nil)
		if err1 != nil {
			s.params.Logger.Errorw("error creating validate request", err1)
			return ErrCannotDialSignal
		}
		validateReq.Header = header
		hresp, err := http.DefaultClient.Do(validateReq)
		if err != nil {
			s.params.Logger.Errorw("error getting validation", err, "httpResponse", hresp)
			return ErrCannotDialSignal
		} else if hresp.StatusCode == http.StatusOK {
			// no specific errors to return if validate succeeds
			s.params.Logger.Infow("validate succeeded")
			return ErrCannotConnectSignal
		} else {
			var errString string
			switch hresp.StatusCode {
			case http.StatusUnauthorized:
				errString = "unauthorized: "
			case http.StatusNotFound:
				errString = "not found: "
			case http.StatusServiceUnavailable:
				errString = "unavailable: "
			}
			body, err := io.ReadAll(hresp.Body)
			if err == nil {
				errString += string(body)
			}
			return errors.New(errString)
		}
	}
	s.Close() // close previous conn, if any
	s.conn.Store(conn)

	// server should send join as soon as connected
	res, err := s.readResponse()
	if err != nil {
		return err
	}

	s.params.SignalHandler.HandleMessage(res)
	return nil
}

func (s *signalTransportWebSocket) websocketConn() *websocket.Conn {
	return s.conn.Load()
}

func (s *signalTransportWebSocket) readWorker(readerClosedCh chan struct{}) {
	defer func() {
		s.isStarted.Store(false)
		s.conn.Store(nil)
		close(readerClosedCh)

		s.params.SignalTransportHandler.OnTransportClose()
	}()
	/* RAJA-TODO
	if pending := c.pendingResponse; pending != nil {
		c.handleResponse(pending)
		c.pendingResponse = nil
	}
	*/
	for {
		res, err := s.readResponse()
		if err != nil {
			if !isIgnoredWebsocketError(err) {
				s.params.Logger.Infow("error while reading from signal client", "error", err)
			}
			return
		}
		s.params.SignalHandler.HandleMessage(res)
	}
}

func (s *signalTransportWebSocket) readResponse() (proto.Message, error) {
	conn := s.websocketConn()
	if conn == nil {
		return nil, errors.New("cannot read response without signal transport")
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

// ----------------------------------

func getStatsParamString() string {
	params := "&sdk=go&os=" + runtime.GOOS
	return params
}

func isIgnoredWebsocketError(err error) bool {
	if err == nil ||
		err == io.EOF ||
		strings.Contains(err.Error(), "use of closed network connection") ||
		strings.Contains(err.Error(), "connection reset by peer") {
		return true
	}

	return websocket.IsCloseError(err, websocket.CloseAbnormalClosure, websocket.CloseGoingAway, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived)
}

/* RAJA-REMOVE
func (c *SignalClient) Start() {
	if c.isStarted.Swap(true) {
		return
	}
	c.readerClosedCh = make(chan struct{})
	go c.readWorker(c.readerClosedCh)
}

func (c *SignalClient) IsStarted() bool {
	return c.isStarted.Load()
}

// Deprecated, use JoinContext
func (c *SignalClient) Join(urlPrefix string, token string, params SignalClientConnectParams) (*livekit.JoinResponse, error) {
	return c.JoinContext(context.TODO(), urlPrefix, token, params)
}

func (c *SignalClient) JoinContext(ctx context.Context, urlPrefix string, token string, params SignalClientConnectParams) (*livekit.JoinResponse, error) {
	res, err := c.connectContext(ctx, urlPrefix, token, params, "")
	if err != nil {
		return nil, err
	}

	join := res.GetJoin()
	if join == nil {
		return nil, fmt.Errorf("unexpected response: %v", res.Message)
	}

	return join, nil
}

// Reconnect starts a new WebSocket connection to the server, passing in reconnect=1
// when successful, it'll return a ReconnectResponse; older versions of the server will not send back a ReconnectResponse
func (c *SignalClient) Reconnect(urlPrefix string, token string, params SignalClientConnectParams, participantSID string) (*livekit.ReconnectResponse, error) {
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

// Deprecated, use connectContext.
func (c *SignalClient) connect(urlPrefix string, token string, params SignalClientConnectParams, participantSID string) (*livekit.SignalResponse, error) {
	return c.connectContext(context.TODO(), urlPrefix, token, params, participantSID)
}

func (c *SignalClient) connectContext(ctx context.Context, urlPrefix string, token string, params SignalClientConnectParams, participantSID string) (*livekit.SignalResponse, error) {
	if urlPrefix == "" {
		return nil, ErrURLNotProvided
	}
	urlPrefix = ToWebsocketURL(urlPrefix)
	urlSuffix := fmt.Sprintf("/rtc?protocol=%d&version=%s", PROTOCOL, Version)

	if params.AutoSubscribe {
		urlSuffix += "&auto_subscribe=1"
	} else {
		urlSuffix += "&auto_subscribe=0"
	}
	if params.Reconnect {
		urlSuffix += "&reconnect=1"
		if participantSID != "" {
			urlSuffix += fmt.Sprintf("&sid=%s", participantSID)
		}
	}
	if len(params.Attributes) != 0 {
		data, err := json.Marshal(params.Attributes)
		if err != nil {
			return nil, ErrInvalidParameter
		}
		str := base64.URLEncoding.EncodeToString(data)
		urlSuffix += "&attributes=" + str
	}
	urlSuffix += getStatsParamString()

	u, err := url.Parse(urlPrefix + urlSuffix)
	if err != nil {
		return nil, err
	}

	header := newHeaderWithToken(token)
	startedAt := time.Now()
	conn, hresp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		fields := []interface{}{
			"duration", time.Since(startedAt),
		}
		if hresp != nil {
			body, _ := io.ReadAll(hresp.Body)
			fields = append(fields, "status", hresp.StatusCode, "response", string(body))
		}
		c.log.Errorw("error establishing signal connection", err, fields...)

		if strings.HasSuffix(err.Error(), ":53: server misbehaving") {
			// DNS issue, abort
			return nil, ErrCannotDialSignal
		}
		// use validate endpoint to get the actual error
		validateSuffix := strings.Replace(urlSuffix, "/rtc", "/rtc/validate", 1)

		validateReq, err1 := http.NewRequestWithContext(ctx, http.MethodGet, ToHttpURL(urlPrefix)+validateSuffix, nil)
		if err1 != nil {
			c.log.Errorw("error creating validate request", err1)
			return nil, ErrCannotDialSignal
		}
		validateReq.Header = header
		hresp, err := http.DefaultClient.Do(validateReq)
		if err != nil {
			c.log.Errorw("error getting validation", err, "httpResponse", hresp)
			return nil, ErrCannotDialSignal
		} else if hresp.StatusCode == http.StatusOK {
			// no specific errors to return if validate succeeds
			c.log.Infow("validate succeeded")
			return nil, ErrCannotConnectSignal
		} else {
			var errString string
			switch hresp.StatusCode {
			case http.StatusUnauthorized:
				errString = "unauthorized: "
			case http.StatusNotFound:
				errString = "not found: "
			case http.StatusServiceUnavailable:
				errString = "unavailable: "
			}
			body, err := io.ReadAll(hresp.Body)
			if err == nil {
				errString += string(body)
			}
			return nil, errors.New(errString)
		}
	}
	c.Close() // close previous conn, if any
	c.conn.Store(conn)

	// server should send join as soon as connected
	res, err := c.readResponse()
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (c *SignalClient) Close() {
	isStarted := c.IsStarted()
	readerClosedCh := c.readerClosedCh
	conn := c.websocketConn()
	if conn != nil {
		_ = conn.Close()
	}
	if isStarted && readerClosedCh != nil {
		<-readerClosedCh
	}
}

func (c *SignalClient) SendICECandidate(candidate webrtc.ICECandidateInit, target livekit.SignalTarget) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Trickle{
			Trickle: ToProtoTrickle(candidate, target),
		},
	})
}

func (c *SignalClient) SendOffer(sd webrtc.SessionDescription) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Offer{
			Offer: ToProtoSessionDescription(sd),
		},
	})
}

func (c *SignalClient) SendAnswer(sd webrtc.SessionDescription) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Answer{
			Answer: ToProtoSessionDescription(sd),
		},
	})
}

func (c *SignalClient) SendMuteTrack(sid string, muted bool) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Mute{
			Mute: &livekit.MuteTrackRequest{
				Sid:   sid,
				Muted: muted,
			},
		},
	})
}

func (c *SignalClient) SendSyncState(state *livekit.SyncState) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_SyncState{
			SyncState: state,
		},
	})
}

func (c *SignalClient) SendLeave() error {
	return c.SendLeaveWithReason(livekit.DisconnectReason_UNKNOWN_REASON)
}

func (c *SignalClient) SendLeaveWithReason(reason livekit.DisconnectReason) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_Leave{
			Leave: &livekit.LeaveRequest{
				Reason: reason,
			},
		},
	})
}

func (c *SignalClient) SendRequest(req *livekit.SignalRequest) error {
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

func (c *SignalClient) SendUpdateTrackSettings(settings *livekit.UpdateTrackSettings) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_TrackSetting{
			TrackSetting: settings,
		},
	})
}

func (c *SignalClient) SendUpdateParticipantMetadata(metadata *livekit.UpdateParticipantMetadata) error {
	return c.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_UpdateMetadata{
			UpdateMetadata: metadata,
		},
	})
}

func getStatsParamString() string {
	params := "&sdk=go&os=" + runtime.GOOS
	return params
}
*/
