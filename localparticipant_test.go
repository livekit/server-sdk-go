// Copyright 2025 LiveKit, Inc.
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

	"github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
)

// TestPrepareSimulcastTrackPublication verifies that publication options passed
// with a simulcast track are propagated into the AddTrackRequest built locally
// before it is sent to the server – in particular the requested encryption.
func TestPrepareSimulcastTrackPublication(t *testing.T) {
	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}

	newParticipant := func(t *testing.T) *LocalParticipant {
		t.Helper()

		publisherTransport, err := NewPCTransport(PCTransportParams{
			Configuration: webrtc.Configuration{},
			Codecs: []webrtc.RTPCodecParameters{{
				RTPCodecCapability: codec,
				PayloadType:        96,
			}},
		})
		require.NoError(t, err)
		// so that peer connection callbacks don't hit a nil logger
		publisherTransport.SetLogger(protoLogger.GetLogger())
		t.Cleanup(func() {
			require.NoError(t, publisherTransport.Close())
		})

		engine := NewRTCEngine(false, nil, nil, nil)
		// PCTransport is closed above; RTCEngine cannot be closed here as it
		// would dereference a nil signal connection, so make sure the transport
		// assigned to the engine is shared with the cleanup above.
		engine.publisher = publisherTransport

		return newLocalParticipant(engine, NewRoomCallback(), &livekit.ServerInfo{}, protoLogger.GetLogger())
	}

	testCases := []struct {
		name       string
		encryption livekit.Encryption_Type
	}{
		{name: "encryption propagated to add track request", encryption: livekit.Encryption_GCM},
		{name: "encryption defaults to none", encryption: livekit.Encryption_NONE},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p := newParticipant(t)

			tracks := newSimulcastSampleTracks(t, codec, "sim_test")
			require.Len(t, tracks, 3)

			opts := &TrackPublicationOptions{
				Name:       "test",
				Source:     livekit.TrackSource_CAMERA,
				Encryption: tc.encryption,
			}

			pub, req, _, err := p.prepareSimulcastTrackPublication(tracks, opts)
			require.NoError(t, err)
			require.NotNil(t, pub)

			// core regression assertion: encryption must be taken from opts
			require.Equal(t, tc.encryption, req.Encryption)

			// sanity: the request should reflect a proper simulcast publication
			require.Equal(t, tracks[2].ID(), req.Cid) // highest layer is the main track
			require.Len(t, req.Layers, 3)
			require.Greater(t, len(req.SimulcastCodecs), 0)
		})
	}
}
