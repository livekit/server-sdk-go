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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"google.golang.org/protobuf/proto"
)

var _ SignalTransport = (*signalTransportHttp)(nil)

type SignalTransportHttpParams struct {
	Logger        logger.Logger
	Version       string
	Protocol      int
	Signalling    Signalling
	SignalHandler SignalHandler
}

type signalTransportHttp struct {
	signalTransportUnimplemented

	params SignalTransportHttpParams

	lock           sync.Mutex
	url            string
	participantSid string
	token          string
}

func NewSignalTransportHttp(params SignalTransportHttpParams) SignalTransport {
	return &signalTransportHttp{
		params: params,
	}
}

func (s *signalTransportHttp) SetLogger(l logger.Logger) {
	s.params.Logger = l
}

func (s *signalTransportHttp) Join(
	ctx context.Context,
	url string,
	token string,
	connectParams ConnectParams,
) error {
	msg, err := s.connect(ctx, url, token, connectParams, "")
	if err != nil {
		return err
	}

	return s.params.SignalHandler.HandleMessage(msg)
}

// SIGNALLING-V2-TODO: fix this comment after reconnect=1 is finalised for signalling v2
// Reconnect sends a new connection request to the server, passing in reconnect=1
// when successful, it'll return a ReconnectResponse;
// older versions of the server will not send back a ReconnectResponse
func (s *signalTransportHttp) Reconnect(
	url string,
	token string,
	connectParams ConnectParams,
	participantSID string,
) error {
	connectParams.Reconnect = true
	msg, err := s.connect(context.TODO(), url, token, connectParams, participantSID)
	if err != nil {
		return err
	}

	return s.params.SignalHandler.HandleMessage(msg)
}

func (s *signalTransportHttp) SetParticipantResource(url string, participantSid string, token string) {
	s.lock.Lock()
	s.url = url
	s.participantSid = participantSid
	s.token = token
	s.lock.Unlock()
}

func (s *signalTransportHttp) UpdateParticipantToken(token string) {
	s.lock.Lock()
	s.token = token
	s.lock.Unlock()
}

// SIGNALLING-V2-TODO: have to write in messageId order
// SIGNALLING-V2-TODO: have to return error
func (s *signalTransportHttp) SendMessage(msg proto.Message) error {
	if msg == nil {
		return nil
	}

	// SIGNALLING-V2-TODO: see note above about ordering and returning error,
	// using a goroutine as message handlers can trigger a message send
	// (example: SDP offer handler sending an answer). In sync transport,
	// that could lead to a chain where the function making the original
	// request has not returned.
	// Potentially need to create a queue, but that makes it async. Needs more thinking.
	go func() {
		s.lock.Lock()
		url := s.url + s.params.Signalling.ParticipantPath(s.participantSid)
		token := s.token
		s.lock.Unlock()

		respWireMessage, err := s.sendHttpRequest(
			url,
			http.MethodPatch,
			token,
			msg,
		)
		if err != nil {
			return
		}

		s.params.SignalHandler.HandleMessage(respWireMessage)
	}()
	return nil
}

func (s *signalTransportHttp) connect(
	ctx context.Context,
	urlPrefix string,
	token string,
	connectParams ConnectParams,
	participantSID string,
) (proto.Message, error) {
	if joinMethod := s.params.Signalling.JoinMethod(); joinMethod != joinMethodConnectRequest {
		// SIGNALLING-V2-TODO: add HTTP support for v1 signalling
		return nil, ErrUnsupportedSignalling
	}

	if urlPrefix == "" {
		return nil, ErrURLNotProvided
	}

	connectRequest, err := s.params.Signalling.ConnectRequest(
		s.params.Version,
		s.params.Protocol,
		&connectParams,
		participantSID,
	)
	if err != nil {
		return nil, err
	}

	wireMessage := s.params.Signalling.SignalConnectRequest(connectRequest)
	return s.sendHttpRequest(
		urlPrefix+s.params.Signalling.Path(),
		http.MethodPost,
		token,
		wireMessage,
	)
}

func (s *signalTransportHttp) sendHttpRequest(
	location string,
	method string,
	token string,
	wireMessage proto.Message,
) (*livekit.Signalv2WireMessage, error) {
	protoMsg, err := proto.Marshal(wireMessage)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(ToHttpURL(location))
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.TODO(), method, u.String(), bytes.NewBuffer(protoMsg))
	if err != nil {
		return nil, err
	}

	req.Header = NewHTTPHeaderWithToken(token)
	req.Header.Set("Content-Type", "application/x-protobuf")

	startedAt := time.Now()
	hresp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.params.Logger.Errorw("http request failed", err, "httpResponse", hresp)
		return nil, err
	}

	s.params.Logger.Infow("http response received", "elapsed", time.Since(startedAt))

	defer hresp.Body.Close()

	body, err := io.ReadAll(hresp.Body)
	if err != nil {
		return nil, err
	}

	if hresp.StatusCode != http.StatusOK {
		return nil, errors.New(s.params.Signalling.DecodeErrorResponse(body))
	}

	if hresp.Header.Get("Content-type") != "application/x-protobuf" {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedContentType, hresp.Header.Get("Content-type"))
	}

	respWireMessage := &livekit.Signalv2WireMessage{}
	if err := proto.Unmarshal(body, respWireMessage); err != nil {
		return nil, err
	}

	return respWireMessage, nil
}
