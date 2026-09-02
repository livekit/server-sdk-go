// Copyright 2026 LiveKit, Inc.
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
	"sync"

	protoLogger "github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
)

const dataTrackBufferedAmountLowThreshold = 8 * 1024

// dataTrackFramePackets is one frame serialized into data track packets.
type dataTrackFramePackets [][]byte

// dataTrackSender paces frames onto the data track channel, keeping only the freshest frame
// while the channel's send buffer is above the low threshold.
type dataTrackSender struct {
	dc  func() *webrtc.DataChannel
	log protoLogger.Logger

	lock   sync.Mutex
	frame  dataTrackFramePackets
	notify chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newDataTrackSender(dc func() *webrtc.DataChannel, log protoLogger.Logger) *dataTrackSender {
	return &dataTrackSender{
		dc:     dc,
		log:    log,
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

func (s *dataTrackSender) setLogger(log protoLogger.Logger) {
	s.log = log
}

func (s *dataTrackSender) send(frame dataTrackFramePackets) {
	s.once.Do(func() { go s.run() })
	if dropped := s.push(frame); dropped != nil {
		s.log.Debugw("dropping data track frame", "packets", len(dropped))
	}
}

func (s *dataTrackSender) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *dataTrackSender) stop() {
	close(s.done)
}

func (s *dataTrackSender) push(frame dataTrackFramePackets) (dropped dataTrackFramePackets) {
	if len(frame) == 0 {
		return nil
	}

	s.lock.Lock()
	dropped, s.frame = s.frame, frame
	s.lock.Unlock()

	s.wake()
	return dropped
}

func (s *dataTrackSender) pop() dataTrackFramePackets {
	s.lock.Lock()
	defer s.lock.Unlock()

	frame := s.frame
	s.frame = nil
	return frame
}

func (s *dataTrackSender) run() {
	var (
		dc       *webrtc.DataChannel
		inFlight dataTrackFramePackets
	)
	for {
		select {
		case <-s.done:
			return
		case <-s.notify:
		}

		for {
			if current := s.dc(); current != dc {
				dc, inFlight = current, nil
			}
			if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen || dc.BufferedAmount() > dataTrackBufferedAmountLowThreshold {
				break
			}
			if len(inFlight) == 0 {
				if inFlight = s.pop(); inFlight == nil {
					break
				}
			}
			if err := dc.Send(inFlight[0]); err != nil {
				s.log.Debugw("could not send data track packet", "error", err)
			}
			inFlight = inFlight[1:]
		}
	}
}
