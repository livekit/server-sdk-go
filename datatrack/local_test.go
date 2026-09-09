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

package datatrack

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/pointer"
	"github.com/stretchr/testify/require"
)

// fakeLocalTransport records what the manager sends, in the order it was sent.
type fakeLocalTransport struct {
	publishRequests   chan *livekit.PublishDataTrackRequest
	unpublishRequests chan *livekit.UnpublishDataTrackRequest
	frames            chan [][]byte
}

func newFakeLocalTransport() *fakeLocalTransport {
	return &fakeLocalTransport{
		publishRequests:   make(chan *livekit.PublishDataTrackRequest, 16),
		unpublishRequests: make(chan *livekit.UnpublishDataTrackRequest, 16),
		frames:            make(chan [][]byte, 16),
	}
}

func (f *fakeLocalTransport) SendPublishRequest(req *livekit.PublishDataTrackRequest) error {
	f.publishRequests <- req
	return nil
}

func (f *fakeLocalTransport) SendUnpublishRequest(req *livekit.UnpublishDataTrackRequest) error {
	f.unpublishRequests <- req
	return nil
}

func (f *fakeLocalTransport) SendFrame(packets [][]byte) {
	f.frames <- packets
}

// prefixingEncryptor marks payloads instead of encrypting them.
type prefixingEncryptor struct{}

func (prefixingEncryptor) Encrypt(payload []byte) ([]byte, E2EEExtension, error) {
	return append([]byte{0xde, 0xad, 0xbe, 0xef}, payload...), E2EEExtension{}, nil
}

// expectEvent receives from ch or fails after the 500 ms the Rust tests allow.
func expectEvent[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(500 * time.Millisecond):
		var zero T
		t.Fatalf("timed out waiting for %T", zero)
		return zero
	}
}

// publishAsync calls Publish on another goroutine and delivers its outcome on the returned channel.
func publishAsync(ctx context.Context, m *LocalManager, name string) <-chan publishResult {
	result := make(chan publishResult, 1)
	go func() {
		track, err := m.Publish(ctx, PublishOptions{Name: name})
		result <- publishResult{track: track, err: err}
	}()
	return result
}

// accepted is the SFU's answer to a publish request.
func accepted(request *livekit.PublishDataTrackRequest, sid SID) *livekit.PublishDataTrackResponse {
	return &livekit.PublishDataTrackResponse{Info: &livekit.DataTrackInfo{
		PubHandle:  request.GetPubHandle(),
		Sid:        string(sid),
		Name:       request.GetName(),
		Encryption: request.GetEncryption(),
	}}
}

func TestLocalPipeline_ProcessFrame(t *testing.T) {
	pipeline := newLocalPipeline(0x8811, nil)

	const repeatedByte = 0xab
	frame := Frame{
		Payload:       bytes.Repeat([]byte{repeatedByte}, 32_000),
		UserTimestamp: pointer.To(uint64(0x4411221111118811)),
	}

	packets, err := pipeline.processFrame(frame)
	require.NoError(t, err)
	require.Len(t, packets, 3)

	for _, raw := range packets {
		packet, err := parsePacket(raw)
		require.NoError(t, err)
		require.Nil(t, extensionsOf(&packet.Header).E2EE)
		require.NotEmpty(t, packet.Payload)
		require.Equal(t, bytes.Repeat([]byte{repeatedByte}, len(packet.Payload)), packet.Payload)
	}
}

func TestLocalManager_Shutdown(t *testing.T) {
	m := NewLocalManager(LocalManagerParams{Transport: newFakeLocalTransport()})
	m.Shutdown()
	require.Empty(t, m.PublishResponsesForSyncState())
}

func TestLocalManager_Publish(t *testing.T) {
	const payloadSize, packetCount = 256, 10
	trackName, trackSID := "track", SID("DTR_1234")

	transport := newFakeLocalTransport()
	m := NewLocalManager(LocalManagerParams{Transport: transport})

	result := publishAsync(context.Background(), m, trackName)

	request := expectEvent(t, transport.publishRequests)
	require.Equal(t, livekit.Encryption_NONE, request.GetEncryption())
	require.Equal(t, trackName, request.GetName())

	// SFU accepts publication
	m.HandlePublishResponse(accepted(request, trackSID))

	res := expectEvent(t, result)
	require.NoError(t, res.err)
	track := res.track
	require.False(t, track.Info().UsesE2EE)
	require.Equal(t, trackName, track.Info().Name)
	require.Equal(t, trackSID, track.Info().SID)

	for range packetCount {
		require.NoError(t, track.TryPush(Frame{Payload: bytes.Repeat([]byte{0xfa}, payloadSize)}))
		time.Sleep(10 * time.Millisecond)
	}
	for range packetCount {
		packets := expectEvent(t, transport.frames)
		packet, err := parsePacket(packets[0])
		require.NoError(t, err)
		require.Len(t, packet.Payload, payloadSize)
	}

	track.Unpublish()
	unpublish := expectEvent(t, transport.unpublishRequests)
	require.Equal(t, request.GetPubHandle(), unpublish.GetPubHandle())
}

func TestLocalManager_PublishSfuError(t *testing.T) {
	transport := newFakeLocalTransport()
	m := NewLocalManager(LocalManagerParams{Transport: transport})

	result := publishAsync(context.Background(), m, "test")

	// SFU rejects publication
	request := expectEvent(t, transport.publishRequests)
	handled := m.HandleRequestResponse(&livekit.RequestResponse{
		Request: &livekit.RequestResponse_PublishDataTrack{PublishDataTrack: request},
		Reason:  livekit.RequestResponse_LIMIT_EXCEEDED,
	})
	require.True(t, handled)

	res := expectEvent(t, result)
	require.ErrorIs(t, res.err, ErrLimitReached)
}

func TestLocalManager_PublishCancelled(t *testing.T) {
	transport := newFakeLocalTransport()
	m := NewLocalManager(LocalManagerParams{Transport: transport})

	ctx, cancel := context.WithCancel(context.Background())
	result := publishAsync(ctx, m, "test")

	request := expectEvent(t, transport.publishRequests)

	// Caller gives up before SFU responds
	cancel()
	res := expectEvent(t, result)
	require.ErrorIs(t, res.err, context.Canceled)
	time.Sleep(50 * time.Millisecond)

	// Late SFU response arrives after cancellation
	m.HandlePublishResponse(accepted(request, "DTR_1234"))

	// Manager sends unpublish for the orphaned handle
	unpublish := expectEvent(t, transport.unpublishRequests)
	require.Equal(t, request.GetPubHandle(), unpublish.GetPubHandle())
}

func TestLocalManager_PublishWithE2EE(t *testing.T) {
	transport := newFakeLocalTransport()
	m := NewLocalManager(LocalManagerParams{Transport: transport, Encryptor: prefixingEncryptor{}})

	result := publishAsync(context.Background(), m, "secure")

	// SFU publish request should indicate e2ee
	request := expectEvent(t, transport.publishRequests)
	require.Equal(t, livekit.Encryption_GCM, request.GetEncryption())

	// SFU accepts publication with e2ee
	m.HandlePublishResponse(accepted(request, "DTR_1234"))

	res := expectEvent(t, result)
	require.NoError(t, res.err)
	track := res.track
	require.True(t, track.Info().UsesE2EE)

	// Push a frame and verify encryption was applied
	require.NoError(t, track.TryPush(Frame{Payload: []byte{1, 2, 3, 4, 5}}))

	packets := expectEvent(t, transport.frames)
	packet, err := parsePacket(packets[0])
	require.NoError(t, err)
	require.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, packet.Payload[:4])
	require.Equal(t, []byte{1, 2, 3, 4, 5}, packet.Payload[4:])
	require.NotNil(t, extensionsOf(&packet.Header).E2EE)
}

func TestLocalManager_RepublishTracks(t *testing.T) {
	transport := newFakeLocalTransport()
	m := NewLocalManager(LocalManagerParams{Transport: transport})

	// Publish a track through the full flow
	trackName, trackSID := "track", SID("DTR_1234")

	result := publishAsync(context.Background(), m, trackName)
	request := expectEvent(t, transport.publishRequests)
	m.HandlePublishResponse(accepted(request, trackSID))

	res := expectEvent(t, result)
	require.NoError(t, res.err)
	track := res.track
	require.Equal(t, trackSID, track.Info().SID)

	// Simulate reconnect
	m.RepublishTracks()
	time.Sleep(50 * time.Millisecond)

	// TryPush should fail while republishing
	require.ErrorIs(t, track.TryPush(Frame{Payload: []byte{0xff}}), ErrQueueFull)

	// SFU re-publishes with a new SID
	request = expectEvent(t, transport.publishRequests)
	require.Equal(t, uint32(track.handle), request.GetPubHandle())
	require.Equal(t, trackName, request.GetName())

	newSID := SID("DTR_5678")
	m.HandlePublishResponse(accepted(request, newSID))
	time.Sleep(50 * time.Millisecond)

	// SID updated in place, pushes succeed again
	require.Equal(t, newSID, track.Info().SID)
	require.NoError(t, track.TryPush(Frame{Payload: []byte{0xff}}))
}

func TestLocalManager_QueryPublished(t *testing.T) {
	transport := newFakeLocalTransport()
	m := NewLocalManager(LocalManagerParams{Transport: transport})

	// Publish two tracks
	for _, name := range []string{"track_a", "track_b"} {
		result := publishAsync(context.Background(), m, name)
		request := expectEvent(t, transport.publishRequests)
		m.HandlePublishResponse(accepted(request, "DTR_1234"))
		require.NoError(t, expectEvent(t, result).err)
	}

	published := m.PublishResponsesForSyncState()
	require.Len(t, published, 2)

	var names []string
	for _, response := range published {
		names = append(names, response.GetInfo().GetName())
	}
	require.Contains(t, names, "track_a")
	require.Contains(t, names, "track_b")
}

func TestLocalManager_ShutdownWithPendingAndActive(t *testing.T) {
	transport := newFakeLocalTransport()
	m := NewLocalManager(LocalManagerParams{Transport: transport})

	// Pending publication (no SFU response sent)
	pending := publishAsync(context.Background(), m, "pending")
	expectEvent(t, transport.publishRequests)

	// Active publication (fully published)
	active := publishAsync(context.Background(), m, "active")
	request := expectEvent(t, transport.publishRequests)
	m.HandlePublishResponse(accepted(request, "DTR_1234"))

	res := expectEvent(t, active)
	require.NoError(t, res.err)
	activeTrack := res.track
	require.True(t, activeTrack.IsPublished())

	// Shutdown the manager
	m.Shutdown()
	time.Sleep(50 * time.Millisecond)

	// Pending publish receives disconnected error
	require.ErrorIs(t, expectEvent(t, pending).err, ErrDisconnected)

	// Active track is no longer published
	require.False(t, activeTrack.IsPublished())
}
