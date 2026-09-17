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

	dtp "github.com/livekit/protocol/datatrack"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/stretchr/testify/require"
)

// fakeRemoteTransport records what the manager sends and emits, in order.
type fakeRemoteTransport struct {
	subscriptionUpdates chan *livekit.UpdateDataSubscription
	published           chan *RemoteTrack
	unpublished         chan *RemoteTrack
}

func newFakeRemoteTransport() *fakeRemoteTransport {
	return &fakeRemoteTransport{
		subscriptionUpdates: make(chan *livekit.UpdateDataSubscription, 16),
		published:           make(chan *RemoteTrack, 16),
		unpublished:         make(chan *RemoteTrack, 16),
	}
}

func (f *fakeRemoteTransport) SendUpdateSubscription(req *livekit.UpdateDataSubscription) error {
	f.subscriptionUpdates <- req
	return nil
}

func (f *fakeRemoteTransport) OnTrackPublished(track *RemoteTrack) {
	f.published <- track
}

func (f *fakeRemoteTransport) OnTrackUnpublished(track *RemoteTrack) {
	f.unpublished <- track
}

// prefixStrippingDecryptor undoes prefixingEncryptor.
type prefixStrippingDecryptor struct{}

func (prefixStrippingDecryptor) Decrypt(payload []byte, _ E2EEExtension) ([]byte, error) {
	return payload[4:], nil
}

// expectNoEvent fails if ch delivers anything within the grace period.
func expectNoEvent[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case event := <-ch:
		t.Fatalf("unexpected event %v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

// expectClosed waits for ch to be closed.
func expectClosed(t *testing.T, ch <-chan Frame) {
	t.Helper()
	select {
	case _, ok := <-ch:
		require.False(t, ok, "expected channel to be closed")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to close")
	}
}

// publishTrack simulates the SFU announcing a publication and returns the track handed to the
// application.
func publishTrack(t *testing.T, m *RemoteManager, transport *fakeRemoteTransport, publisherIdentity string, info Info) *RemoteTrack {
	t.Helper()
	m.handlePublicationUpdates(map[string][]Info{publisherIdentity: {info}})
	return expectEvent(t, transport.published)
}

func expectSubscriptionUpdate(t *testing.T, transport *fakeRemoteTransport) (SID, bool) {
	t.Helper()
	msg := expectEvent(t, transport.subscriptionUpdates)
	require.Len(t, msg.GetUpdates(), 1)
	update := msg.GetUpdates()[0]
	return SID(update.GetTrackSid()), update.GetSubscribe()
}

// assignHandle simulates the SFU assigning a subscriber handle.
func assignHandle(m *RemoteManager, handle trackHandle, sid SID) {
	m.HandleSubscriberHandles(&livekit.DataTrackSubscriberHandles{
		SubHandles: map[uint32]*livekit.DataTrackSubscriberHandles_PublishedDataTrack{
			uint32(handle): {TrackSid: string(sid)},
		},
	})
}

// subscribeAsync calls Subscribe on another goroutine and delivers its outcome on the returned channel.
func subscribeAsync(ctx context.Context, track *RemoteTrack, opts ...SubscribeOption) <-chan subscribeResult {
	result := make(chan subscribeResult, 1)
	go func() {
		stream, err := track.Subscribe(ctx, opts...)
		result <- subscribeResult{stream: stream, err: err}
	}()
	return result
}

// subscribeAndActivate subscribes, answers the SFU request with handle, and returns the stream.
func subscribeAndActivate(t *testing.T, m *RemoteManager, transport *fakeRemoteTransport, track *RemoteTrack, handle trackHandle) *Stream {
	t.Helper()
	result := subscribeAsync(context.Background(), track)

	sid, subscribe := expectSubscriptionUpdate(t, transport)
	require.True(t, subscribe)
	require.Equal(t, track.Info().SID, sid)

	assignHandle(m, handle, sid)

	res := expectEvent(t, result)
	require.NoError(t, res.err)
	return res.stream
}

// rawPacket marshals a packet derived from the Rust vector header.
func rawPacket(t *testing.T, marker FrameMarker, handle trackHandle, sequence, frameNumber uint16, payload []byte, extensions Extensions) []byte {
	t.Helper()
	header := testHeader()
	marker.apply(&header)
	header.Handle = uint16(handle)
	header.SequenceNumber = sequence
	header.FrameNumber = frameNumber
	extensions.apply(&header)
	packet := dtp.Packet{Header: header, Payload: payload}
	raw, err := packet.Marshal()
	require.NoError(t, err)
	return raw
}

func singlePacket(t *testing.T, handle trackHandle, payload []byte, extensions Extensions) []byte {
	return rawPacket(t, FrameMarkerSingle, handle, 0, 0, payload, extensions)
}

// pushInterleavedTwoFramePair pushes Start(frame1), Start(frame2), Final(frame1), Final(frame2)
// through the manager to exercise the depacketizer's concurrent partial frame handling.
func pushInterleavedTwoFramePair(t *testing.T, m *RemoteManager, handle trackHandle, frameOne, frameOneStart uint16, frameOnePayloads [2][]byte, frameTwo, frameTwoStart uint16, frameTwoPayloads [2][]byte) {
	t.Helper()
	push := func(frameNumber, sequence uint16, marker FrameMarker, payload []byte) {
		m.HandlePacket(rawPacket(t, marker, handle, sequence, frameNumber, payload, Extensions{}))
	}
	push(frameOne, frameOneStart, FrameMarkerStart, frameOnePayloads[0])
	push(frameTwo, frameTwoStart, FrameMarkerStart, frameTwoPayloads[0])
	push(frameOne, frameOneStart+1, FrameMarkerFinal, frameOnePayloads[1])
	push(frameTwo, frameTwoStart+1, FrameMarkerFinal, frameTwoPayloads[1])
}

func TestRemotePipeline_ProcessPacket(t *testing.T) {
	const payloadLen = 1024
	pipeline := newRemotePipeline(nil, logger.GetLogger())

	header := testHeader()
	FrameMarkerSingle.apply(&header)
	packet := &dtp.Packet{Header: header, Payload: bytes.Repeat([]byte{0xab}, payloadLen)}

	frame, ok := pipeline.processPacket(packet, defaultMaxPartialFrames)
	require.True(t, ok, "should return a frame")
	require.Len(t, frame.Payload, payloadLen)
}

func TestRemoteManager_Shutdown(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	track := publishTrack(t, m, transport, "id", Info{SID: "DTR_1234", pubHandle: 1, Name: "test"})
	pending := subscribeAsync(context.Background(), track)
	expectSubscriptionUpdate(t, transport)

	m.Shutdown()

	require.ErrorIs(t, expectEvent(t, pending).err, ErrDisconnected)
	require.False(t, track.IsPublished())
	expectEvent(t, track.Unpublished())
}

func TestRemoteManager_Subscribe(t *testing.T) {
	publisherIdentity, trackName, trackSID := "publisher", "track", SID("DTR_1234")
	subHandle := trackHandle(0x1234)

	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	// Simulate track published
	track := publishTrack(t, m, transport, publisherIdentity, Info{SID: trackSID, pubHandle: 1, Name: trackName})
	require.True(t, track.IsPublished())
	require.Equal(t, trackName, track.Info().Name)
	require.Equal(t, trackSID, track.Info().SID)
	require.Equal(t, publisherIdentity, track.PublisherIdentity())

	result := subscribeAsync(context.Background(), track)

	sid, subscribe := expectSubscriptionUpdate(t, transport)
	require.True(t, subscribe)
	require.Equal(t, trackSID, sid)
	time.Sleep(20 * time.Millisecond)

	// Simulate SFU reply
	assignHandle(m, subHandle, trackSID)

	res := expectEvent(t, result)
	require.NoError(t, res.err)
	require.NotNil(t, res.stream)
}

func TestRemoteManager_TrackPublicationAddAndRemove(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	trackSID := SID("DTR_1234")

	// Simulate track published
	track := publishTrack(t, m, transport, "identity1", Info{SID: trackSID, pubHandle: 1, Name: "test"})
	require.Equal(t, trackSID, track.Info().SID)
	require.Equal(t, "test", track.Info().Name)
	require.True(t, track.IsPublished())

	// Simulate track unpublished
	m.handlePublicationUpdates(map[string][]Info{"identity1": nil})

	expectEvent(t, track.Unpublished())
	require.False(t, track.IsPublished())

	unpublished := expectEvent(t, transport.unpublished)
	require.Equal(t, trackSID, unpublished.Info().SID)
}

func TestRemoteManager_SfuPublicationUpdatesIdempotent(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	info := Info{SID: "DTR_1234", pubHandle: 1, Name: "test"}

	// Simulate three identical publication updates
	for range 3 {
		m.handlePublicationUpdates(map[string][]Info{"identity1": {info}})
	}

	expectEvent(t, transport.published)

	// No second publication should appear
	m.Shutdown()
	expectNoEvent(t, transport.published)
}

func TestRemoteManager_SidReassignmentDoesNotRepublish(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	pubHandle := trackHandle(7)
	oldSID, newSID := SID("DTR_1234"), SID("DTR_5678")

	// Simulate track published
	track := publishTrack(t, m, transport, "id", Info{SID: oldSID, pubHandle: pubHandle, Name: "test"})
	require.Equal(t, oldSID, track.Info().SID)

	// Simulate publisher full reconnect: same track, new SID
	m.handlePublicationUpdates(map[string][]Info{"id": {{SID: newSID, pubHandle: pubHandle, Name: "test"}}})

	// No publish/unpublish should appear
	m.Shutdown()
	expectNoEvent(t, transport.published)
	expectNoEvent(t, transport.unpublished)
	require.Equal(t, newSID, track.Info().SID)
}

func TestRemoteManager_SidReassignmentResubscribesActiveSubscription(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	pubHandle := trackHandle(7)
	oldSID, newSID := SID("DTR_1234"), SID("DTR_5678")
	oldSubHandle, newSubHandle := trackHandle(0x1001), trackHandle(0x1002)

	// Simulate track published
	track := publishTrack(t, m, transport, "id", Info{SID: oldSID, pubHandle: pubHandle, Name: "test"})

	// Subscribe to the track
	stream := subscribeAndActivate(t, m, transport, track, oldSubHandle)

	// Simulate publisher full reconnect: same track, new SID
	m.handlePublicationUpdates(map[string][]Info{"id": {{SID: newSID, pubHandle: pubHandle, Name: "test"}}})

	// Manager should re-subscribe under the new SID
	sid, subscribe := expectSubscriptionUpdate(t, transport)
	require.True(t, subscribe)
	require.Equal(t, newSID, sid)
	require.Equal(t, newSID, track.Info().SID)
	require.True(t, track.IsPublished())

	// Simulate SFU assigning a new subscriber handle
	assignHandle(m, newSubHandle, newSID)

	// Frames received on the new handle reach the existing subscriber
	m.HandlePacket(singlePacket(t, newSubHandle, []byte{1, 2, 3, 4, 5}, Extensions{}))

	frame := expectEvent(t, stream.Frames())
	require.Equal(t, []byte{1, 2, 3, 4, 5}, frame.Payload)
}

func TestRemoteManager_SubscribeReceivesFrame(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	trackSID, subHandle := SID("DTR_1234"), trackHandle(0x1234)

	// Simulate track published
	track := publishTrack(t, m, transport, "id", Info{SID: trackSID, pubHandle: 1, Name: "test"})

	// Subscribe to the track
	stream := subscribeAndActivate(t, m, transport, track, subHandle)

	// Simulate receiving a single-frame packet
	m.HandlePacket(singlePacket(t, subHandle, []byte{1, 2, 3, 4, 5}, Extensions{}))

	frame := expectEvent(t, stream.Frames())
	require.Equal(t, []byte{1, 2, 3, 4, 5}, frame.Payload)
}

func TestRemoteManager_SubscribeWithE2EE(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport, Decryptor: prefixStrippingDecryptor{}})

	trackSID, subHandle := SID("DTR_1234"), trackHandle(0x1234)

	// Simulate track published (with e2ee)
	track := publishTrack(t, m, transport, "id", Info{SID: trackSID, pubHandle: 1, Name: "test", UsesE2EE: true})

	// Subscribe to the track
	stream := subscribeAndActivate(t, m, transport, track, subHandle)

	// Simulate receiving an encrypted single-frame packet
	payload := []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5}
	m.HandlePacket(singlePacket(t, subHandle, payload, Extensions{E2EE: &E2EEExtension{}}))

	// Payload should have fake encryption prefix stripped by decryptor
	frame := expectEvent(t, stream.Frames())
	require.Equal(t, []byte{1, 2, 3, 4, 5}, frame.Payload)
}

func TestRemoteManager_SubscribeFanOutToMultipleSubscribers(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	trackSID, subHandle := SID("DTR_1234"), trackHandle(0x1234)

	// Simulate track published
	track := publishTrack(t, m, transport, "id", Info{SID: trackSID, pubHandle: 1, Name: "test"})

	// First subscriber triggers SFU interaction
	stream1 := subscribeAndActivate(t, m, transport, track, subHandle)

	// Additional subscribers attach directly (no further SFU interaction)
	stream2, err := track.Subscribe(context.Background())
	require.NoError(t, err)
	stream3, err := track.Subscribe(context.Background())
	require.NoError(t, err)
	expectNoEvent(t, transport.subscriptionUpdates)

	// Simulate receiving a single-frame packet
	m.HandlePacket(singlePacket(t, subHandle, []byte{1, 2, 3, 4, 5}, Extensions{}))

	// All subscribers should receive the same frame
	for _, stream := range []*Stream{stream1, stream2, stream3} {
		frame := expectEvent(t, stream.Frames())
		require.Equal(t, []byte{1, 2, 3, 4, 5}, frame.Payload)
	}
}

func TestRemoteManager_SubscribeUnknownTrackFails(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	// A track that is no longer published cannot be subscribed to
	track := publishTrack(t, m, transport, "id", Info{SID: "DTR_1234", pubHandle: 1, Name: "test"})
	m.handlePublicationUpdates(map[string][]Info{"id": nil})
	expectEvent(t, transport.unpublished)

	_, err := track.Subscribe(context.Background())
	require.ErrorIs(t, err, ErrUnpublished)
}

func TestRemoteManager_UnpublishTerminatesPendingSubscription(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	trackSID := SID("DTR_1234")

	// Simulate track published
	track := publishTrack(t, m, transport, "id", Info{SID: trackSID, pubHandle: 1, Name: "test"})

	// Subscribe (enters pending state)
	result := subscribeAsync(context.Background(), track)
	_, subscribe := expectSubscriptionUpdate(t, transport)
	require.True(t, subscribe)

	// Simulate track unpublished before SFU assigns a handle
	m.handlePublicationUpdates(map[string][]Info{"id": nil})

	require.ErrorIs(t, expectEvent(t, result).err, ErrUnpublished)

	unpublished := expectEvent(t, transport.unpublished)
	require.Equal(t, trackSID, unpublished.Info().SID)
}

func TestRemoteManager_UnpublishTerminatesActiveSubscription(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	trackSID, subHandle := SID("DTR_1234"), trackHandle(0x1234)

	// Simulate track published
	track := publishTrack(t, m, transport, "id", Info{SID: trackSID, pubHandle: 1, Name: "test"})

	// Subscribe to the track
	stream := subscribeAndActivate(t, m, transport, track, subHandle)

	// Simulate track unpublished while subscription is active
	m.handlePublicationUpdates(map[string][]Info{"id": nil})

	expectClosed(t, stream.Frames())

	unpublished := expectEvent(t, transport.unpublished)
	require.Equal(t, trackSID, unpublished.Info().SID)
}

func TestRemoteManager_AllSubscribersDroppedTerminatesSfuSubscription(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	trackSID, subHandle := SID("DTR_1234"), trackHandle(0x1234)

	// Simulate track published
	track := publishTrack(t, m, transport, "id", Info{SID: trackSID, pubHandle: 1, Name: "test"})

	// Subscribe to the track
	stream := subscribeAndActivate(t, m, transport, track, subHandle)

	// Close the only subscriber
	stream.Close()

	// Manager should request SFU to unsubscribe
	sid, subscribe := expectSubscriptionUpdate(t, transport)
	require.False(t, subscribe)
	require.Equal(t, trackSID, sid)
}

// Should depacketize multiple interleaved partial frames when MaxPartialFrames is set before subscribe.
func TestRemoteManager_MaxPartialFramesSetBeforeSubscribe(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	subHandle := trackHandle(0x1234)
	track := publishTrack(t, m, transport, "id", Info{SID: "DTR_1234", pubHandle: 1, Name: "test"})

	// Configure the track BEFORE any subscribe
	track.SetPipelineOptions(PipelineOptions{MaxPartialFrames: 3})

	stream := subscribeAndActivate(t, m, transport, track, subHandle)

	// Two interleaved partial frames: Start(1), Start(2), Final(1), Final(2). With the default
	// MaxPartialFrames of 1 frame 1 would be evicted by frame 2; with 3 both frames coexist and emerge.
	pushInterleavedTwoFramePair(t, m, subHandle, 1, 0, [2][]byte{{0xa1}, {0xa2}}, 2, 100, [2][]byte{{0xb1}, {0xb2}})

	require.Equal(t, []byte{0xa1, 0xa2}, expectEvent(t, stream.Frames()).Payload)
	require.Equal(t, []byte{0xb1, 0xb2}, expectEvent(t, stream.Frames()).Payload)
}

// Should pick up MaxPartialFrames live on an already-active subscription.
func TestRemoteManager_MaxPartialFramesSetLive(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	subHandle := trackHandle(0x1234)
	track := publishTrack(t, m, transport, "id", Info{SID: "DTR_1234", pubHandle: 1, Name: "test"})

	stream := subscribeAndActivate(t, m, transport, track, subHandle)

	// Subscription is now active; flip the cap on the live pipeline
	track.SetPipelineOptions(PipelineOptions{MaxPartialFrames: 3})

	pushInterleavedTwoFramePair(t, m, subHandle, 1, 0, [2][]byte{{0xa1}, {0xa2}}, 2, 100, [2][]byte{{0xb1}, {0xb2}})

	require.Equal(t, []byte{0xa1, 0xa2}, expectEvent(t, stream.Frames()).Payload)
	require.Equal(t, []byte{0xb1, 0xb2}, expectEvent(t, stream.Frames()).Payload)
}

// Should drop the older partial frame by default (no MaxPartialFrames set).
func TestRemoteManager_DefaultDropsOlderPartialFrame(t *testing.T) {
	transport := newFakeRemoteTransport()
	m := NewRemoteManager(RemoteManagerParams{Transport: transport})

	subHandle := trackHandle(0x1234)
	track := publishTrack(t, m, transport, "id", Info{SID: "DTR_1234", pubHandle: 1, Name: "test"})

	stream := subscribeAndActivate(t, m, transport, track, subHandle)

	// Default cap of 1: Start(2) evicts Start(1), so Final(1) is unknown and only frame 2 makes it through
	pushInterleavedTwoFramePair(t, m, subHandle, 1, 0, [2][]byte{{0xa1}, {0xa2}}, 2, 100, [2][]byte{{0xb1}, {0xb2}})

	require.Equal(t, []byte{0xb1, 0xb2}, expectEvent(t, stream.Frames()).Payload)
	expectNoEvent(t, stream.Frames())
}
