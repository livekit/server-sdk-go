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
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func testFrame(marker byte, packets int) dataTrackFramePackets {
	frame := make(dataTrackFramePackets, packets)
	for i := range frame {
		frame[i] = []byte{marker, byte(i)}
	}
	return frame
}

func TestDataTrackSenderQueue(t *testing.T) {
	s := newDataTrackSender(func() *webrtc.DataChannel { return nil }, logger)
	t.Cleanup(s.stop)

	require.Nil(t, s.push(nil))
	require.Nil(t, s.pop())

	multi := testFrame(0xaa, 13)
	require.Nil(t, s.push(multi))
	require.Equal(t, multi, s.pop())

	older, newer := testFrame(0x01, 4), testFrame(0x02, 3)
	require.Nil(t, s.push(older))
	require.Equal(t, older, s.push(newer))
	require.Equal(t, newer, s.pop())
	require.Nil(t, s.pop())
}

func TestSendDataTrackFrameKeepsFreshest(t *testing.T) {
	engine := newTestEngine(t)

	engine.sendDataTrackFrame(testFrame(0x01, 2))
	engine.sendDataTrackFrame(testFrame(0x02, 2))
	require.Equal(t, testFrame(0x02, 2), engine.dataTrackSender.pop())
}
