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
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
	"github.com/stretchr/testify/require"

	"github.com/livekit/server-sdk-go/v2/datatrack"
	"github.com/livekit/server-sdk-go/v2/e2ee"
)

// testRoom is one connection to the shared test room; published is fed by OnDataTrackPublished.
type testRoom struct {
	room      *Room
	published <-chan *datatrack.RemoteTrack
}

// testRoomOptions configure one connection, the counterpart of Rust's TestRoomOptions.
type testRoomOptions struct {
	grants  auth.VideoGrant
	connect []ConnectOption
}

func defaultTestRoomOptions() testRoomOptions {
	return testRoomOptions{grants: auth.VideoGrant{RoomJoin: true}}
}

// testRooms creates count connections to a shared room.
func testRooms(t *testing.T, count int) []testRoom {
	options := make([]testRoomOptions, count)
	for i := range options {
		options[i] = defaultTestRoomOptions()
	}
	return testRoomsWithOptions(t, options...)
}

// testRoomsWithOptions creates one connection per option to a shared room and waits until every
// participant sees the others.
func testRoomsWithOptions(t *testing.T, options ...testRoomOptions) []testRoom {
	t.Helper()
	roomName := fmt.Sprintf("test_room_%s", uuid.NewString())

	rooms := make([]testRoom, 0, len(options))
	for id, option := range options {
		option.grants.Room = roomName
		token, err := auth.NewAccessToken(apiKey, apiSecret).
			SetValidFor(30 * time.Minute).
			SetVideoGrant(&option.grants).
			SetIdentity(fmt.Sprintf("p%d", id)).
			SetName(fmt.Sprintf("Participant %d", id)).
			ToJWT()
		require.NoError(t, err, "Failed to generate JWT")

		published := make(chan *datatrack.RemoteTrack, 16)
		callback := &RoomCallback{ParticipantCallback: ParticipantCallback{
			OnDataTrackPublished: func(track *datatrack.RemoteTrack, _ *RemoteParticipant) {
				published <- track
			},
		}}
		room, err := ConnectToRoomWithToken(host, token, callback, option.connect...)
		require.NoError(t, err, "Failed to connect to room")
		t.Cleanup(room.Disconnect)
		rooms = append(rooms, testRoom{room: room, published: published})
	}

	// Wait for participant visibility across all room connections. When using a local SFU, this
	// takes significantly longer and can lead to intermittently failing tests.
	allConnected := time.Now()
	require.Eventually(t, func() bool {
		for _, r := range rooms {
			if len(r.room.GetRemoteParticipants()) != len(rooms)-1 {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond, "Not all participants became visible")
	t.Logf("All participants visible after %v", time.Since(allConnected))

	return rooms
}

// waitForRemoteTrack waits for the first remote data track to be published.
func waitForRemoteTrack(t *testing.T, r testRoom) *datatrack.RemoteTrack {
	t.Helper()
	select {
	case track := <-r.published:
		return track
	case <-time.After(5 * time.Second):
		t.Fatal("No track published")
		return nil
	}
}

// receiveFrame waits for the next frame on the stream.
func receiveFrame(t *testing.T, stream *datatrack.Stream, timeout time.Duration) datatrack.Frame {
	t.Helper()
	select {
	case frame, ok := <-stream.Frames():
		require.True(t, ok, "Stream closed before a frame was received")
		return frame
	case <-time.After(timeout):
		t.Fatal("No frame received")
		return datatrack.Frame{}
	}
}

// pushEvery pushes payload on track at the given interval until ctx ends.
func pushEvery(ctx context.Context, track *datatrack.LocalTrack, payload []byte, interval time.Duration) {
	for {
		_ = track.TryPush(datatrack.Frame{Payload: payload})
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func TestDataTrack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		payloadLen int
	}{
		{"single_packet", 8_192},
		{"multi_packet", 196_608},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rooms := testRooms(t, 2)
			pubRoom, subRoom := rooms[1], rooms[0]
			pubIdentity := pubRoom.room.LocalParticipant.Identity()

			localTrack, err := pubRoom.room.LocalParticipant.PublishDataTrack(context.Background(), "my_track")
			require.NoError(t, err)
			t.Log("Track published")

			remoteTrack := waitForRemoteTrack(t, subRoom)
			t.Logf("Got remote track: %s", remoteTrack.Info().SID)

			const payloadValue = 0xfa

			require.True(t, localTrack.IsPublished())
			require.False(t, localTrack.Info().UsesE2EE)
			require.Equal(t, "my_track", localTrack.Info().Name)

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			go pushEvery(ctx, localTrack, bytes.Repeat([]byte{payloadValue}, tc.payloadLen), 50*time.Millisecond)

			require.True(t, remoteTrack.IsPublished())
			require.False(t, remoteTrack.Info().UsesE2EE)
			require.Equal(t, "my_track", remoteTrack.Info().Name)
			require.Equal(t, pubIdentity, remoteTrack.PublisherIdentity())

			stream, err := remoteTrack.Subscribe(ctx)
			require.NoError(t, err)
			defer stream.Close()

			frame := receiveFrame(t, stream, 15*time.Second)
			require.Len(t, frame.Payload, tc.payloadLen)
			require.Equal(t, bytes.Repeat([]byte{payloadValue}, tc.payloadLen), frame.Payload)
			require.Nil(t, frame.UserTimestamp)
			require.True(t, remoteTrack.IsPublished())
		})
	}
}

func TestDataTrack_PublishManyTracks(t *testing.T) {
	const trackCount = 256

	room := testRooms(t, 1)[0].room

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tracks := make([]*datatrack.LocalTrack, 0, trackCount)
	start := time.Now()
	for idx := range trackCount {
		name := fmt.Sprintf("track_%d", idx)
		track, err := room.LocalParticipant.PublishDataTrack(ctx, name)
		require.NoError(t, err)

		require.True(t, track.IsPublished())
		require.Equal(t, name, track.Info().Name)

		tracks = append(tracks, track)
	}
	elapsed := time.Since(start)
	t.Logf("Publishing %d tracks took %v (average %v per track)", trackCount, elapsed, elapsed/trackCount)

	for _, track := range tracks {
		// Publish a single large frame per track.
		require.NoError(t, track.TryPush(datatrack.Frame{Payload: bytes.Repeat([]byte{0xfa}, 196_608)}))
	}
}

func TestDataTrack_PublishUnauthorized(t *testing.T) {
	options := defaultTestRoomOptions()
	options.grants.SetCanPublishData(false)
	room := testRoomsWithOptions(t, options)[0].room

	_, err := room.LocalParticipant.PublishDataTrack(context.Background(), "my_track")
	require.ErrorIs(t, err, datatrack.ErrNotAllowed)
}

func TestDataTrack_PublishDuplicateName(t *testing.T) {
	room := testRooms(t, 1)[0].room

	first, err := room.LocalParticipant.PublishDataTrack(context.Background(), "first")
	require.NoError(t, err)
	defer first.Unpublish()

	_, err = room.LocalParticipant.PublishDataTrack(context.Background(), "first")
	require.ErrorIs(t, err, datatrack.ErrDuplicateName)
}

func TestDataTrack_PublishWithSchemaMetadata(t *testing.T) {
	for _, tc := range []struct {
		name           string
		schemaEncoding datatrack.SchemaEncoding
		frameEncoding  datatrack.FrameEncoding
	}{
		{"well_known", datatrack.SchemaEncodingJSONSchema, datatrack.FrameEncodingJSON},
		{"custom", datatrack.CustomSchemaEncoding("a"), datatrack.CustomFrameEncoding("b")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rooms := testRooms(t, 2)
			pubRoom, subRoom := rooms[1], rooms[0]

			schemaID := datatrack.SchemaID{Name: "my_schema", Encoding: tc.schemaEncoding}
			localTrack, err := pubRoom.room.LocalParticipant.PublishDataTrack(context.Background(), "my_track",
				datatrack.WithSchema(schemaID), datatrack.WithFrameEncoding(tc.frameEncoding))
			require.NoError(t, err)
			require.Equal(t, &schemaID, localTrack.Info().Schema)
			require.Equal(t, tc.frameEncoding, localTrack.Info().FrameEncoding)

			// The subscriber should observe the same schema and frame encoding metadata.
			remoteTrack := waitForRemoteTrack(t, subRoom)
			require.Equal(t, &schemaID, remoteTrack.Info().Schema)
			require.Equal(t, tc.frameEncoding, remoteTrack.Info().FrameEncoding)
		})
	}
}

func TestDataTrack_E2EE(t *testing.T) {
	const sharedSecret = "password"
	payload := bytes.Repeat([]byte{0xfa}, 196_608)

	encrypted := func() testRoomOptions {
		keyProvider := e2ee.NewExternalKeyProvider()
		require.NoError(t, keyProvider.SetKeyFromPassphrase(sharedSecret, 0))
		options := defaultTestRoomOptions()
		options.connect = []ConnectOption{WithDataEncryption(&EncryptionOptions{KeyProvider: keyProvider})}
		return options
	}
	rooms := testRoomsWithOptions(t, encrypted(), encrypted())
	pubRoom, subRoom := rooms[1], rooms[0]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := pubRoom.room.LocalParticipant.PublishDataTrack(ctx, "my_track")
	require.NoError(t, err)
	require.True(t, track.Info().UsesE2EE)
	go pushEvery(ctx, track, payload, 125*time.Millisecond)

	remoteTrack := waitForRemoteTrack(t, subRoom)
	require.True(t, remoteTrack.Info().UsesE2EE)
	stream, err := remoteTrack.Subscribe(ctx)
	require.NoError(t, err)
	defer stream.Close()

	frame := receiveFrame(t, stream, 5*time.Second)
	require.Equal(t, payload, frame.Payload)
}

func TestDataTrack_PublishedState(t *testing.T) {
	// How long to leave the track published.
	const publishDuration = 500 * time.Millisecond

	rooms := testRooms(t, 2)
	pubRoom, subRoom := rooms[1], rooms[0]

	track, err := pubRoom.room.LocalParticipant.PublishDataTrack(context.Background(), "my_track")
	require.NoError(t, err)
	require.True(t, track.IsPublished())

	remoteTrack := waitForRemoteTrack(t, subRoom)
	require.True(t, remoteTrack.IsPublished())

	start := time.Now()
	time.AfterFunc(publishDuration, track.Unpublish)

	select {
	case <-remoteTrack.Unpublished():
	case <-time.After(5 * time.Second):
		t.Fatal("Track was not unpublished")
	}
	elapsed := time.Since(start)
	require.InDelta(t, publishDuration, elapsed, float64(20*time.Millisecond))
	require.False(t, remoteTrack.IsPublished())
}

func TestDataTrack_Resubscribe(t *testing.T) {
	const iterations = 10

	rooms := testRooms(t, 2)
	pubRoom, subRoom := rooms[1], rooms[0]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := pubRoom.room.LocalParticipant.PublishDataTrack(ctx, "my_track")
	require.NoError(t, err)
	go pushEvery(ctx, track, bytes.Repeat([]byte{0xfa}, 64), 50*time.Millisecond)

	remoteTrack := waitForRemoteTrack(t, subRoom)

	successfulSubscriptions := 0
	for range iterations {
		stream, err := remoteTrack.Subscribe(ctx)
		require.NoError(t, err)

		// Ensure we can at least get one frame.
		frame := receiveFrame(t, stream, 5*time.Second)
		require.NotEmpty(t, frame.Payload)
		successfulSubscriptions++

		stream.Close()
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, iterations, successfulSubscriptions)
}

func TestDataTrack_FrameWithUserTimestamp(t *testing.T) {
	rooms := testRooms(t, 2)
	pubRoom, subRoom := rooms[1], rooms[0]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	track, err := pubRoom.room.LocalParticipant.PublishDataTrack(ctx, "my_track")
	require.NoError(t, err)
	go func() {
		for {
			_ = track.TryPush(datatrack.Frame{Payload: bytes.Repeat([]byte{0xfa}, 64), UserTimestamp: datatrack.UserTimestampNow()})
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()

	remoteTrack := waitForRemoteTrack(t, subRoom)
	stream, err := remoteTrack.Subscribe(ctx)
	require.NoError(t, err)
	defer stream.Close()

	// Ensure we can at least get one frame.
	frame := receiveFrame(t, stream, 5*time.Second)
	require.NotEmpty(t, frame.Payload)
	duration, ok := frame.DurationSinceTimestamp()
	require.True(t, ok, "Missing timestamp")
	require.Less(t, duration, time.Second)
}

// faultScenarios mirrors Rust's SignalReconnect and ForceTcp cases. Pion's ForceTCP simulation is a
// no-op, so the full reconnect is driven by SimulateNodeFailure instead.
var faultScenarios = []struct {
	name          string
	scenario      SimulateScenario
	fullReconnect bool
}{
	{"signal_reconnect", SimulateSignalReconnect, false},
	{"full_reconnect", SimulateNodeFailure, true},
}

func TestDataTrack_SubscriberSideFault(t *testing.T) {
	for _, tc := range faultScenarios {
		t.Run(tc.name, func(t *testing.T) {
			rooms := testRooms(t, 2)
			pubRoom, subRoom := rooms[1], rooms[0]

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			track, err := pubRoom.room.LocalParticipant.PublishDataTrack(ctx, "my_track")
			require.NoError(t, err)
			go pushEvery(ctx, track, bytes.Repeat([]byte{0xfa}, 64), 50*time.Millisecond)

			remoteTrack := waitForRemoteTrack(t, subRoom)
			stream, err := remoteTrack.Subscribe(ctx)
			require.NoError(t, err)
			defer stream.Close()

			// TODO: this should also evaluate what happens if a track subscription is removed
			// during a full reconnect event.
			subRoom.room.Simulate(tc.scenario)
			require.True(t, remoteTrack.IsPublished())

			// Ensure we can at least get one frame.
			frame := receiveFrame(t, stream, 15*time.Second)
			require.NotEmpty(t, frame.Payload)
		})
	}
}

func TestDataTrack_PublisherSideFault(t *testing.T) {
	for _, tc := range faultScenarios {
		t.Run(tc.name, func(t *testing.T) {
			pubRoom := testRooms(t, 1)[0]

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			track, err := pubRoom.room.LocalParticipant.PublishDataTrack(ctx, "my_track")
			require.NoError(t, err)
			initialSID := track.Info().SID

			pubRoom.room.Simulate(tc.scenario)
			require.True(t, track.IsPublished(), "Should still be reported as published")

			if tc.fullReconnect {
				// Republish (full reconnect → new session → new sid) is async. Poll up to 8s for
				// the new sid instead of unconditionally sleeping a fixed window.
				require.Eventually(t, func() bool {
					return track.Info().SID != initialSID
				}, 8*time.Second, 100*time.Millisecond, "Should have new SID after full reconnect (still %s)", initialSID)
			}

			require.True(t, track.IsPublished(), "Should still be reported as published")
			require.NoError(t, track.TryPush(datatrack.Frame{Payload: bytes.Repeat([]byte{0xfa}, 64)}), "Should be able to push frame")
		})
	}
}
