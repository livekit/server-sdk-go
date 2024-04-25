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
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

func TestSynchronizer(t *testing.T) {
	s := NewSynchronizer(nil)
	tt := newTrackTester(s, webrtc.RTPCodecTypeVideo)

	// first frame (SN:100,101 PTS:0ms)
	tt.testPacket(t)
	tt.sn++
	tt.testPacket(t)

	// next frame
	tt.testNextFrame(t, true) // (SN:102,103 PTS:41.7ms)

	// sequence number jump
	tt.sn += 4000
	tt.testNextFrame(t, true) // (SN:104,105 PTS:83.3ms)

	// dropped packets
	tt.expectNextFrame(true) // (SN:- PTS:125ms)
	tt.expectNextFrame(true) // (SN:- PTS:166.7ms)
	tt.sn += 6
	tt.testNextFrame(t, true) // (SN:112,113 PTS: 208.3ms)

	// sequence number and timestamp jump
	tt.sn += 6000
	tt.timestamp += 1234567
	tt.testNextFrame(t, true) // (SN:114,115 PTS: 250ms)

	// normal frames
	tt.testNextFrame(t, true) // (SN:116,117 PTS:291.7ms)
	tt.testNextFrame(t, true) // (SN:118,119 PTS:333.3ms)

	// sequence number and timestamp jump
	tt.sn += 5000
	tt.timestamp += 7654321
	tt.testNextFrame(t, true) // (SN:120,121 PTS:375ms)
	tt.testNextFrame(t, true) // (SN:122,123 PTS:416.7ms)

	// mute
	tt.testBlankFrame(t) // (SN:124 PTS:458.3ms)
	tt.testBlankFrame(t) // (SN:125 PTS:500ms)
	tt.testBlankFrame(t) // (SN:126 PTS:541.7ms)
	tt.testBlankFrame(t) // (SN:127 PTS:583.3ms)

	// unmute
	tt.testNextFrame(t, true) // (SN:128,129 PTS:625ms)
	tt.testNextFrame(t, true) // (SN:130,131 PTS:666.7ms)

	// mute
	tt.testBlankFrame(t) // (SN:132 PTS:708.3ms)
	tt.testBlankFrame(t) // (SN:133 PTS:750ms)
	tt.testBlankFrame(t) // (SN:134 PTS:791.7ms)
	tt.testBlankFrame(t) // (SN:135 PTS:833.3ms)

	// unmute with sequence number and timestamp jump
	tt.sn += 3333
	tt.timestamp += 33333333
	tt.testNextFrame(t, true) // (SN:136,137 PTS:875ms)
	tt.testNextFrame(t, true) // (SN:138,139 PTS:916.7ms)

	require.Equal(t, time.Duration(math.Round(1e9/24)), tt.ts.GetFrameDuration())
}

func TestMultipleTracks(t *testing.T) {
	audioOnlyDuration := time.Minute
	if testing.Short() {
		audioOnlyDuration = time.Second
	}

	s := NewSynchronizer(nil)
	var tt2 *trackTester
	time.AfterFunc(audioOnlyDuration, func() {
		tt2 = newTrackTester(s, webrtc.RTPCodecTypeVideo)
	})
	tt1 := newTrackTester(s, webrtc.RTPCodecTypeAudio)

	// audio only
	audioFrames := int(audioOnlyDuration.Seconds() * 50)
	for i := 0; i < audioFrames; i++ {
		tt1.testNextFrame(t, true)
	}

	// late video track
	tt2.adjustExpected(t, audioOnlyDuration)
	for i := 0; i < 600; i++ {
		time.Sleep(time.Microsecond * 1660)
		if i%12 == 0 {
			// 50 audio frames
			tt1.testNextFrame(t, false)
		}
		if i%25 == 0 {
			// 24 video frames
			tt2.testNextFrame(t, false)
		}
	}
}

type trackTester struct {
	i           int
	ts          *TrackSynchronizer
	sn          uint16
	timestamp   uint32
	expectedPTS time.Duration

	frameDurationRTP uint32
	frameDurationPTS time.Duration
}

func newTrackTester(s *Synchronizer, kind webrtc.RTPCodecType) *trackTester {
	track := newFakeTrack(kind)
	tt := &trackTester{
		ts: s.AddTrack(track, "fake"),
	}

	if kind == webrtc.RTPCodecTypeAudio {
		// 20ms audio frames
		tt.sn = 55555
		tt.timestamp = 55555555
		tt.frameDurationRTP = 960
		tt.frameDurationPTS = time.Duration(20000000)
	} else {
		// 24 fps
		tt.sn = 100
		tt.timestamp = 10000
		tt.frameDurationRTP = 3750
		tt.frameDurationPTS = time.Duration(41666666)
	}

	tt.ts.stats.AvgSampleDuration = float64(tt.frameDurationRTP)
	tt.ts.Initialize(&rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: tt.sn,
			Timestamp:      tt.timestamp,
		},
	})

	return tt
}

func (tt *trackTester) expectNextFrame(sleep bool) {
	tt.timestamp += tt.frameDurationRTP
	tt.expectedPTS += tt.frameDurationPTS
	if sleep {
		time.Sleep(tt.frameDurationPTS)
	}
	tt.i++
	if tt.frameDurationRTP == 3750 && tt.i%3 != 1 {
		tt.expectedPTS += time.Duration(1)
	}
}

func (tt *trackTester) testNextFrame(t require.TestingT, sleep bool) {
	// new frame
	tt.expectNextFrame(sleep)
	tt.sn++
	tt.testPacket(t)

	// next packet, same frame
	tt.sn++
	tt.testPacket(t)
}

func (tt *trackTester) testBlankFrame(t require.TestingT) {
	tt.expectNextFrame(true)
	pts := tt.ts.InsertFrame(&rtp.Packet{})
	require.InDelta(t, tt.expectedPTS, pts, 1)
}

func (tt *trackTester) adjustExpected(t require.TestingT, estimatedPTS time.Duration) {
	pts, err := tt.ts.GetPTS(&rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: tt.sn,
			Timestamp:      tt.timestamp,
		},
	})
	require.NoError(t, err)
	require.InDelta(t, estimatedPTS, pts, float64(estimatedPTS/10))
	tt.expectedPTS = pts
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

type fakeTrack struct {
	trackID string
	kind    webrtc.RTPCodecType
	ssrc    webrtc.SSRC
}

func newFakeTrack(kind webrtc.RTPCodecType) *fakeTrack {
	return &fakeTrack{
		trackID: fmt.Sprintf("track_%d", rand.Intn(100)),
		kind:    kind,
		ssrc:    webrtc.SSRC(rand.Uint32()),
	}
}

func (t *fakeTrack) ID() string {
	return t.trackID
}

func (t *fakeTrack) Codec() webrtc.RTPCodecParameters {
	if t.kind == webrtc.RTPCodecTypeAudio {
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  "audio/opus",
				ClockRate: 48000,
			},
		}
	} else {
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  "video/vp8",
				ClockRate: 90000,
			},
		}
	}
}

func (t *fakeTrack) Kind() webrtc.RTPCodecType {
	return t.kind
}

func (t *fakeTrack) SSRC() webrtc.SSRC {
	return t.ssrc
}
