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
	"math/rand"
	"sync"

	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/proto"
)

var _ SignalTransport = (*signalTransportDataChannel)(nil)

type SignalTransportDataChannelParams struct {
	Logger        logger.Logger
	DataChannel   *webrtc.DataChannel
	SignalHandler SignalHandler
}

type signalTransportDataChannel struct {
	signalTransportUnimplemented

	params SignalTransportDataChannelParams

	lock             sync.RWMutex
	isStarted        bool
	msgChan          chan proto.Message
	workerGeneration atomic.Uint32
}

func NewSignalTransportDataChannel(params SignalTransportDataChannelParams) SignalTransport {
	s := &signalTransportDataChannel{
		params:  params,
		msgChan: make(chan proto.Message, 100),
	}
	s.workerGeneration.Store(uint32(rand.Intn(1<<8) + 1))
	s.params.DataChannel.OnMessage(func(dataMsg webrtc.DataChannelMessage) {
		s.params.SignalHandler.HandleEncodedMessage(dataMsg.Data)
	})
	return s
}

func (s *signalTransportDataChannel) SetLogger(l logger.Logger) {
	s.params.Logger = l
}

func (s *signalTransportDataChannel) Start() {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.isStarted {
		return
	}
	s.isStarted = true

	go s.worker(s.workerGeneration.Inc())
}

func (s *signalTransportDataChannel) IsStarted() bool {
	s.lock.RLock()
	defer s.lock.RUnlock()

	return s.isStarted
}

func (s *signalTransportDataChannel) Close() {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.isStarted = false
	s.workerGeneration.Inc()
	close(s.msgChan)
}

func (s *signalTransportDataChannel) SendMessage(msg proto.Message) error {
	if msg == nil {
		return nil
	}

	s.lock.RLock()
	defer s.lock.RUnlock()
	if !s.isStarted {
		return ErrTransportNotStarted
	}

	select {
	case s.msgChan <- msg:
		return nil
	default:
		// channel is full
		return ErrTransportChannelFull
	}
}

func (s *signalTransportDataChannel) worker(gen uint32) {
	for gen == s.workerGeneration.Load() {
		msg := <-s.msgChan
		if msg != nil {
			protoMsg, err := proto.Marshal(msg)
			if err != nil {
				s.params.Logger.Errorw("could not marshal proto message", err)
				continue
			}

			if err := s.params.DataChannel.Send(protoMsg); err != nil {
				s.params.Logger.Errorw("could not send proto message", err)
				continue
			}
		}
	}
}
