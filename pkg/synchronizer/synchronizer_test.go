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

package synchronizer

import (
	"math"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

const (
	frameDurationRTP = 3750                    // 24 fps at 90k clock rate
	frameDurationPTS = time.Duration(41666666) // 1/24 of a second
)

func TestSynchronizer(t *testing.T) {
	s := NewSynchronizer(nil)
	tt := newTrackTester(s)

	// first frame (SN:100,101 PTS:0ms)
	tt.testPacket(t)
	tt.sn++
	tt.testPacket(t)

	// next frame
	tt.testNextFrame(t) // (SN:102,103 PTS:41.7ms)

	// sequence number jump
	tt.sn += 4000
	tt.testNextFrame(t) // (SN:104,105 PTS:83.3ms)

	// dropped packets
	tt.nextFrame() // (SN:- PTS:125ms)
	tt.nextFrame() // (SN:- PTS:166.7ms)
	tt.sn += 6
	tt.testNextFrame(t) // (SN:112,113 PTS: 208.3ms)

	// sequence number and timestamp jump
	tt.sn += 6000
	tt.timestamp += 1234567
	tt.testNextFrame(t) // (SN:114,115 PTS: 250ms)

	// normal frames
	tt.testNextFrame(t) // (SN:116,117 PTS:291.7ms)
	tt.testNextFrame(t) // (SN:118,119 PTS:333.3ms)

	// sequence number and timestamp jump
	tt.sn += 5000
	tt.timestamp += 7654321
	tt.testNextFrame(t) // (SN:120,121 PTS:375ms)
	tt.testNextFrame(t) // (SN:122,123 PTS:416.7ms)

	// mute
	tt.testBlankFrame(t) // (SN:124 PTS:458.3ms)
	tt.testBlankFrame(t) // (SN:125 PTS:500ms)
	tt.testBlankFrame(t) // (SN:126 PTS:541.7ms)
	tt.testBlankFrame(t) // (SN:127 PTS:583.3ms)

	// unmute
	tt.testNextFrame(t) // (SN:128,129 PTS:625ms)
	tt.testNextFrame(t) // (SN:130,131 PTS:666.7ms)

	// mute
	tt.testBlankFrame(t) // (SN:132 PTS:708.3ms)
	tt.testBlankFrame(t) // (SN:133 PTS:750ms)
	tt.testBlankFrame(t) // (SN:134 PTS:791.7ms)
	tt.testBlankFrame(t) // (SN:135 PTS:833.3ms)

	// unmute with sequence number and timestamp jump
	tt.sn += 3333
	tt.timestamp += 33333333
	tt.testNextFrame(t) // (SN:136,137 PTS:875ms)
	tt.testNextFrame(t) // (SN:138,139 PTS:916.7ms)

	require.Equal(t, time.Duration(math.Round(1e9/24)), tt.ts.GetFrameDuration())

}

type trackTester struct {
	i           int
	ts          *TrackSynchronizer
	sn          uint16
	timestamp   uint32
	expectedPTS time.Duration
}

func newTrackTester(s *Synchronizer) *trackTester {
	tt := &trackTester{
		ts:          s.AddTrack(&fakeTrack{}, "fake"),
		sn:          100,
		timestamp:   10000,
		expectedPTS: 0,
	}

	tt.ts.stats.AvgSampleDuration = 3750
	tt.ts.Initialize(&rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: tt.sn,
			Timestamp:      tt.timestamp,
		},
	})

	return tt
}

func (tt *trackTester) nextFrame() {
	tt.timestamp += frameDurationRTP
	tt.expectedPTS += frameDurationPTS
	tt.i++
	if tt.i%3 != 1 {
		tt.expectedPTS += time.Duration(00000001)
	}
}

func (tt *trackTester) testNextFrame(t require.TestingT) {
	// new frame
	tt.nextFrame()
	tt.sn++
	tt.testPacket(t)

	// next packet, same frame
	tt.sn++
	tt.testPacket(t)
}

func (tt *trackTester) testBlankFrame(t require.TestingT) {
	tt.nextFrame()
	pts := tt.ts.InsertFrame(&rtp.Packet{})
	require.InDelta(t, tt.expectedPTS, pts, 1)
}

func (tt *trackTester) testPacket(t require.TestingT) {
	pts, err := tt.ts.GetPTS(&rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: tt.sn,
			Timestamp:      tt.timestamp,
		},
	})
	require.NoError(t, err)
	require.InDelta(t, tt.expectedPTS, pts, 1)
}

type fakeTrack struct{}

func (t *fakeTrack) ID() string {
	return "trackID"
}

func (t *fakeTrack) Codec() webrtc.RTPCodecParameters {
	return webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  "video/vp8",
			ClockRate: 90000,
		},
	}
}

func (t *fakeTrack) Kind() webrtc.RTPCodecType {
	return webrtc.RTPCodecTypeVideo
}

func (t *fakeTrack) SSRC() webrtc.SSRC {
	return 1234
}
