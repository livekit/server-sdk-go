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
	"sync"

	"github.com/livekit/protocol/logger"
	"google.golang.org/protobuf/proto"
)

type messageQueueParams struct {
	Logger        logger.Logger
	HandleMessage func(msg proto.Message)
}

type messageQueue struct {
	params messageQueueParams

	lock      sync.RWMutex
	isStarted bool
	msgChan   chan proto.Message
}

func newMessageQueue(params messageQueueParams) *messageQueue {
	return &messageQueue{
		params: params,
	}
}

func (m *messageQueue) SetLogger(l logger.Logger) {
	m.params.Logger = l
}

func (m *messageQueue) Start() {
	m.lock.Lock()
	defer m.lock.Unlock()

	if m.isStarted {
		return
	}
	m.isStarted = true

	m.msgChan = make(chan proto.Message, 100)
	go m.worker(m.msgChan)
}

func (m *messageQueue) IsStarted() bool {
	m.lock.RLock()
	defer m.lock.RUnlock()

	return m.isStarted
}

func (m *messageQueue) Close() {
	m.lock.Lock()
	defer m.lock.Unlock()

	if !m.isStarted {
		return
	}
	m.isStarted = false

	close(m.msgChan)
}

func (m *messageQueue) Enqueue(msg proto.Message) error {
	if msg == nil {
		return nil
	}

	m.lock.RLock()
	defer m.lock.RUnlock()
	if !m.isStarted {
		return ErrMessageQueueNotStarted
	}

	select {
	case m.msgChan <- msg:
		return nil
	default:
		return ErrMessageQueueFull
	}
}

func (m *messageQueue) worker(msgChan chan proto.Message) {
	for {
		msg, more := <-msgChan
		if !more {
			return
		}

		m.params.HandleMessage(msg)
	}
}
