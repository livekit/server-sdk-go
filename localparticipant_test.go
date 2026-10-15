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

// newTestLocalParticipant creates a LocalParticipant backed by a publisher
// transport but no signal connection, for exercising local publication logic.
func newTestLocalParticipant(t *testing.T, codec webrtc.RTPCodecCapability, roomCallback *RoomCallback) *LocalParticipant {
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

	return newLocalParticipant(engine, roomCallback, &livekit.ServerInfo{}, protoLogger.GetLogger())
}

// TestPrepareSimulcastTrackPublication verifies that publication options passed
// with a simulcast track are propagated into the AddTrackRequest built locally
// before it is sent to the server – in particular the requested encryption.
func TestPrepareSimulcastTrackPublication(t *testing.T) {
	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}

	newParticipant := func(t *testing.T) *LocalParticipant {
		return newTestLocalParticipant(t, codec, NewRoomCallback())
	}

	testCases := []struct {
		name       string
		encryption livekit.Encryption_Type
	}{
		{name: "encryption propagated to add track request", encryption: livekit.Encryption_GCM},
		{name: "encryption defaults to none", encryption: livekit.Encryption_NONE},
	}

	for _, tc := range testCases {
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

	t.Run("backup codec with encryption", func(t *testing.T) {
		p := newParticipant(t)

		tracks := newSimulcastSampleTracks(t, codec, "sim_test")
		// use a different codec capability for the backup tracks than the
		// primary; the prepare path only reads Codec().MimeType from them
		backupCodec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}
		backupTracks := newSimulcastSampleTracks(t, backupCodec, "sim_backup_test")
		require.Len(t, backupTracks, 3)

		opts := &TrackPublicationOptions{
			Name:       "test",
			Source:     livekit.TrackSource_CAMERA,
			Encryption: livekit.Encryption_GCM,
		}

		pub, req, _, err := p.prepareSimulcastTrackPublication(
			tracks, opts, WithBackupCodecForSimulcastTrack(backupTracks),
		)
		require.NoError(t, err)

		// backup codec must be ignored when encryption is requested
		require.Len(t, req.SimulcastCodecs, 1) // only the primary codec entry
		_, backupTracksOnPub := pub.getBackupCodecTrack()
		require.Len(t, backupTracksOnPub, 0)
	})

	t.Run("backup codec without encryption", func(t *testing.T) {
		p := newParticipant(t)

		tracks := newSimulcastSampleTracks(t, codec, "sim_test")
		// use a different codec capability for the backup tracks than the
		// primary; the prepare path only reads Codec().MimeType from them
		backupCodec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}
		backupTracks := newSimulcastSampleTracks(t, backupCodec, "sim_backup_test")
		require.Len(t, backupTracks, 3)

		opts := &TrackPublicationOptions{
			Name:       "test",
			Source:     livekit.TrackSource_CAMERA,
			Encryption: livekit.Encryption_NONE,
		}

		pub, req, _, err := p.prepareSimulcastTrackPublication(
			tracks, opts, WithBackupCodecForSimulcastTrack(backupTracks),
		)
		require.NoError(t, err)

		// backup codec must be kept when no encryption is requested
		require.Len(t, req.SimulcastCodecs, 2) // primary + backup codec entries
		require.Len(t, req.SimulcastCodecs[1].Layers, 3)
		_, backupTracksOnPub := pub.getBackupCodecTrack()
		require.Len(t, backupTracksOnPub, 3)
	})
}

// TestLocalTrackSubscribedQualityUpdateCallback verifies that
// OnLocalTrackSubscribedQualityUpdate reports every SubscribedQualityUpdate the
// server sends for a local publication, once per update and after it has been
// applied to the layers, and that it is not invoked by the local publish path
// (which re-enables every layer, on first publish and on re-publish) or for
// updates about unknown tracks.
func TestLocalTrackSubscribedQualityUpdateCallback(t *testing.T) {
	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	tracks := newSimulcastSampleTracks(t, codec, "sim_quality_update")
	require.Len(t, tracks, 3) // [LOW, MEDIUM, HIGH]
	high := tracks[2]

	type call struct {
		pub          *LocalTrackPublication
		codecs       []*livekit.SubscribedCodec
		lp           *LocalParticipant
		highDisabled bool
	}
	var participantCalls, roomCalls []call

	roomCallback := NewRoomCallback()
	roomCallback.OnLocalTrackSubscribedQualityUpdate = func(pub *LocalTrackPublication, codecs []*livekit.SubscribedCodec, lp *LocalParticipant) {
		roomCalls = append(roomCalls, call{pub, codecs, lp, high.disabled.Load()})
	}
	p := newTestLocalParticipant(t, codec, roomCallback)
	p.Callback.OnLocalTrackSubscribedQualityUpdate = func(pub *LocalTrackPublication, codecs []*livekit.SubscribedCodec, lp *LocalParticipant) {
		participantCalls = append(participantCalls, call{pub, codecs, lp, high.disabled.Load()})
	}

	update := func(trackSid string, highEnabled bool) *livekit.SubscribedQualityUpdate {
		return &livekit.SubscribedQualityUpdate{
			TrackSid: trackSid,
			SubscribedCodecs: []*livekit.SubscribedCodec{{
				Codec: "vp8",
				Qualities: []*livekit.SubscribedQuality{
					{Quality: livekit.VideoQuality_LOW, Enabled: true},
					{Quality: livekit.VideoQuality_MEDIUM, Enabled: false},
					{Quality: livekit.VideoQuality_HIGH, Enabled: highEnabled},
				},
			}},
		}
	}

	// the local publish path re-enables every layer; that is not a server update
	pub, _, _, err := p.prepareSimulcastTrackPublication(tracks, &TrackPublicationOptions{
		Name:   "screen",
		Source: livekit.TrackSource_SCREEN_SHARE,
	})
	require.NoError(t, err)
	// shaped like the TrackPublishedResponse: the mime type is only on the per-codec info
	pub.updateInfo(&livekit.TrackInfo{
		Sid:    "TR_quality_update",
		Name:   "screen",
		Type:   livekit.TrackType_VIDEO,
		Codecs: []*livekit.SimulcastCodecInfo{{MimeType: codec.MimeType}},
	})
	p.addPublication(pub)
	require.Empty(t, participantCalls)
	require.Empty(t, roomCalls)

	// an update for a track the participant does not know about is not reported
	p.handleSubscribedQualityUpdate(update("TR_unknown", true))
	require.Empty(t, participantCalls)
	require.Empty(t, roomCalls)

	// each server update is reported exactly once, after it has been applied
	first := update(pub.SID(), false)
	p.handleSubscribedQualityUpdate(first)
	require.Len(t, participantCalls, 1)
	require.Len(t, roomCalls, 1)
	for _, c := range []call{participantCalls[0], roomCalls[0]} {
		require.Same(t, pub, c.pub)
		require.Same(t, p, c.lp)
		require.Equal(t, first.SubscribedCodecs, c.codecs)
		require.True(t, c.highDisabled, "callback must observe the update already applied")
	}

	// an update that does not change any layer is still reported
	p.handleSubscribedQualityUpdate(update(pub.SID(), false))
	require.Len(t, participantCalls, 2)
	require.Len(t, roomCalls, 2)

	p.handleSubscribedQualityUpdate(update(pub.SID(), true))
	require.Len(t, participantCalls, 3)
	require.Len(t, roomCalls, 3)
	require.False(t, participantCalls[2].highDisabled)

	// re-publishing the same tracks re-enables every layer locally; still no report
	_, _, _, err = p.prepareSimulcastTrackPublication(tracks, &TrackPublicationOptions{
		Name:   "screen",
		Source: livekit.TrackSource_SCREEN_SHARE,
	})
	require.NoError(t, err)
	require.Len(t, participantCalls, 3)
	require.Len(t, roomCalls, 3)
}
