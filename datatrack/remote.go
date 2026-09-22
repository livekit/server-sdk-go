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
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	dtp "github.com/livekit/protocol/datatrack"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const (
	defaultBufferSize       = 16
	defaultMaxPartialFrames = 1
	packetBufferCount       = 16
	subscribeTimeout        = 10 * time.Second
)

// RemoteTransport carries what the remote manager produces: subscription requests to the SFU and
// publication events for the application.
type RemoteTransport interface {
	SendUpdateSubscription(req *livekit.UpdateDataSubscription) error
	OnTrackPublished(track *RemoteTrack)
	OnTrackUnpublished(track *RemoteTrack)
}

// Decryptor opens end-to-end encrypted frame payloads.
type Decryptor interface {
	Decrypt(payload []byte, e2ee E2EEExtension) ([]byte, error)
}

// SubscribeOptions configure a subscription.
type SubscribeOptions struct {
	// BufferSize is the number of received frames buffered for the subscriber; older frames are
	// dropped when it is exceeded. Zero is clamped to one.
	BufferSize int
}

// SubscribeOption customizes a subscription.
type SubscribeOption func(*SubscribeOptions)

// WithBufferSize sets the number of received frames buffered for the subscriber. It has no
// effect when the track already has an active subscription.
func WithBufferSize(frames int) SubscribeOption {
	return func(options *SubscribeOptions) {
		options.BufferSize = frames
	}
}

// PipelineOptions configure how a remote track's packets are reassembled.
type PipelineOptions struct {
	// MaxPartialFrames is the number of frames reassembled concurrently. Higher values tolerate
	// more reordering at the cost of buffering. Zero is clamped to one.
	MaxPartialFrames int
}

type RemoteManagerParams struct {
	Transport RemoteTransport
	// Decryptor opens frames of tracks that use end-to-end encryption; nil disables decryption.
	Decryptor Decryptor
	Logger    logger.Logger
}

// RemoteManager tracks the data tracks published by remote participants and the local
// participant's subscriptions to them. Methods are safe for concurrent use.
type RemoteManager struct {
	params RemoteManagerParams

	// mu guards descriptors and subHandles. Each RemoteTrack guards its own state: the manager
	// reaches it through methods, which may take the track's lock while mu is held, and a track
	// never calls into the manager while holding its own lock. Methods suffixed Locked expect mu.
	mu          sync.Mutex
	descriptors map[SID]*RemoteTrack
	subHandles  map[trackHandle]*RemoteTrack
}

func NewRemoteManager(params RemoteManagerParams) *RemoteManager {
	if params.Logger == nil {
		params.Logger = logger.GetLogger()
	}
	return &RemoteManager{
		params:      params,
		descriptors: make(map[SID]*RemoteTrack),
		subHandles:  make(map[trackHandle]*RemoteTrack),
	}
}

// HandleParticipantUpdate applies the data tracks listed for each participant in a
// ParticipantUpdate; tracks a participant no longer lists are unpublished.
func (m *RemoteManager) HandleParticipantUpdate(participants []*livekit.ParticipantInfo, localIdentity string) {
	m.handlePublicationUpdates(publicationUpdatesFromProto(participants, localIdentity, m.params.Logger))
}

// HandleParticipantSnapshot applies a complete list of the room's participants, as carried by a
// JoinResponse. Publishers absent from the list are treated as gone.
func (m *RemoteManager) HandleParticipantSnapshot(participants []*livekit.ParticipantInfo, localIdentity string) {
	updates := publicationUpdatesFromProto(participants, localIdentity, m.params.Logger)
	m.mu.Lock()
	for _, track := range m.descriptors {
		if _, present := updates[track.publisherIdentity]; !present {
			updates[track.publisherIdentity] = nil
		}
	}
	m.mu.Unlock()
	m.handlePublicationUpdates(updates)
}

func (m *RemoteManager) handlePublicationUpdates(updates map[string][]Info) {
	if len(updates) == 0 {
		return
	}
	var published, unpublished []*RemoteTrack
	var resubscribe []subscriptionUpdate

	m.mu.Lock()
	for publisherIdentity, infos := range updates {
		sidsInUpdate := make(map[SID]struct{}, len(infos))
		for _, info := range infos {
			sidsInUpdate[info.SID] = struct{}{}
			if _, known := m.descriptors[info.SID]; known {
				continue
			}
			if update, reassigned := m.reassignSIDLocked(publisherIdentity, info); reassigned {
				if update != nil {
					resubscribe = append(resubscribe, *update)
				}
				continue
			}
			track := newRemoteTrack(m, info, publisherIdentity)
			m.descriptors[info.SID] = track
			published = append(published, track)
		}

		for sid, track := range m.descriptors {
			if track.publisherIdentity != publisherIdentity {
				continue
			}
			if _, present := sidsInUpdate[sid]; !present {
				delete(m.descriptors, sid)
				if handle, wasActive := track.end(ErrUnpublished); wasActive {
					delete(m.subHandles, handle)
				}
				unpublished = append(unpublished, track)
			}
		}
	}
	m.mu.Unlock()

	for _, update := range resubscribe {
		m.sendSubscriptionUpdate(update)
	}
	for _, track := range published {
		m.params.Transport.OnTrackPublished(track)
	}
	for _, track := range unpublished {
		m.params.Transport.OnTrackUnpublished(track)
	}
}

// reassignSIDLocked detects a track republished under a new SID after its publisher's full
// reconnect: publisher identity and handle are stable across republications. It reports whether
// the SID was reassigned and, if a subscription must be re-requested under the new SID, the
// update to send.
func (m *RemoteManager) reassignSIDLocked(publisherIdentity string, info Info) (*subscriptionUpdate, bool) {
	var track *RemoteTrack
	for _, candidate := range m.descriptors {
		if candidate.publisherIdentity == publisherIdentity && candidate.pubHandle == info.pubHandle {
			track = candidate
			break
		}
	}
	if track == nil {
		return nil, false
	}
	oldSID, subscribed, ok := track.reassignSID(info)
	if !ok {
		return nil, false
	}
	delete(m.descriptors, oldSID)
	m.descriptors[info.SID] = track
	if subscribed {
		// the SFU does not carry subscriptions across the publisher's full reconnect
		return &subscriptionUpdate{sid: info.SID, subscribe: true}, true
	}
	return nil, true
}

func schemaEqual(a, b *SchemaID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// HandleSubscriberHandles records the handles the SFU assigned to requested subscriptions, which
// activates pending subscriptions.
func (m *RemoteManager) HandleSubscriberHandles(msg *livekit.DataTrackSubscriberHandles) {
	mapping, err := subscriberHandlesFromProto(msg)
	if err != nil {
		m.params.Logger.Warnw("ignoring invalid data track subscriber handles", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for handle, sid := range mapping {
		track, known := m.descriptors[sid]
		if !known {
			m.params.Logger.Warnw("subscriber handle for unknown data track", nil, "sid", sid)
			continue
		}
		previous, replaced, ok := track.activate(handle)
		if !ok {
			m.params.Logger.Warnw("subscriber handle for data track without subscription", nil, "sid", sid)
			continue
		}
		if replaced {
			delete(m.subHandles, previous)
		}
		m.subHandles[handle] = track
	}
}

// releaseHandle forgets the handle of a subscription the track ended, unless the SFU has since
// assigned the same handle to the track again.
func (m *RemoteManager) releaseHandle(track *RemoteTrack, handle trackHandle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subHandles[handle] == track && !track.usesHandle(handle) {
		delete(m.subHandles, handle)
	}
}

// HandlePacket routes a packet received on the data channel to its track's subscribers.
func (m *RemoteManager) HandlePacket(data []byte) {
	packet, err := parsePacket(data)
	if err != nil {
		m.params.Logger.Warnw("dropping invalid data track packet", err)
		return
	}

	handle := trackHandle(packet.Handle)
	m.mu.Lock()
	track := m.subHandles[handle]
	m.mu.Unlock()
	if track == nil {
		m.params.Logger.Debugw("dropping data track packet without subscription", "handle", packet.Handle)
		return
	}
	track.deliver(handle, packet)
}

// ResendSubscriptionUpdates re-requests every pending and active subscription after a full
// reconnect.
func (m *RemoteManager) ResendSubscriptionUpdates() {
	m.mu.Lock()
	var updates []subscriptionUpdate
	for _, track := range m.descriptors {
		if sid, subscribed := track.subscribedSID(); subscribed {
			updates = append(updates, subscriptionUpdate{sid: sid, subscribe: true})
		}
	}
	m.mu.Unlock()

	for _, update := range updates {
		m.sendSubscriptionUpdate(update)
	}
}

// Shutdown ends every subscription and marks every track unpublished. The manager can be used
// again for a new session.
func (m *RemoteManager) Shutdown() {
	m.mu.Lock()
	for _, track := range m.descriptors {
		track.end(ErrDisconnected)
	}
	clear(m.descriptors)
	clear(m.subHandles)
	m.mu.Unlock()
}

func (m *RemoteManager) sendSubscriptionUpdate(update subscriptionUpdate) {
	if err := m.params.Transport.SendUpdateSubscription(update.toProto()); err != nil {
		m.params.Logger.Warnw("could not send data track subscription update", err, "sid", update.sid, "subscribe", update.subscribe)
	}
}

type subscriptionState int

const (
	subscriptionNone subscriptionState = iota
	subscriptionPending
	subscriptionActive
)

type subscribeResult struct {
	stream *Stream
	err    error
}

// RemoteTrack is a data track published by a remote participant. Methods are safe for concurrent
// use.
type RemoteTrack struct {
	manager           *RemoteManager
	publisherIdentity string
	pubHandle         trackHandle
	unpublished       core.Fuse
	maxPartialFrames  atomic.Int64

	// streamList is an immutable snapshot of streams, read by the worker without the lock
	streamList atomic.Pointer[[]*Stream]

	// mu guards the fields below. It is never held while calling into the manager. Methods
	// suffixed Locked expect it.
	mu           sync.Mutex
	info         Info
	subscription subscriptionState
	waiters      []chan subscribeResult
	bufferSize   int
	subHandle    trackHandle
	// packets feeds the worker goroutine of the active subscription
	packets chan *dtp.Packet
	streams map[*Stream]struct{}
}

func newRemoteTrack(manager *RemoteManager, info Info, publisherIdentity string) *RemoteTrack {
	track := &RemoteTrack{
		manager:           manager,
		publisherIdentity: publisherIdentity,
		pubHandle:         info.pubHandle,
		info:              info,
		streams:           make(map[*Stream]struct{}),
	}
	track.maxPartialFrames.Store(defaultMaxPartialFrames)
	track.streamList.Store(&[]*Stream{})
	return track
}

// Info returns a snapshot of the track's metadata. The SID changes when the publisher completes a
// full reconnect.
func (t *RemoteTrack) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.info
}

// PublisherIdentity is the identity of the participant who published the track.
func (t *RemoteTrack) PublisherIdentity() string {
	return t.publisherIdentity
}

// IsPublished reports whether the track is still published.
func (t *RemoteTrack) IsPublished() bool {
	return !t.unpublished.IsBroken()
}

// Unpublished is closed once the track is no longer published, whether by its publisher, the SFU,
// or disconnecting from the room.
func (t *RemoteTrack) Unpublished() <-chan struct{} {
	return t.unpublished.Watch()
}

// SetPipelineOptions configures how the track's packets are reassembled. The options apply to all
// current and future subscriptions and take effect with the next packet.
func (t *RemoteTrack) SetPipelineOptions(options PipelineOptions) {
	if options.MaxPartialFrames < 1 {
		t.manager.params.Logger.Warnw("zero is not a valid value for MaxPartialFrames, using one", nil)
		options.MaxPartialFrames = 1
	}
	t.maxPartialFrames.Store(int64(options.MaxPartialFrames))
}

// Subscribe starts receiving the track's frames. Only the first subscription talks to the SFU;
// later ones share the pipeline and miss frames delivered before they were made. A 10 second
// deadline applies when ctx has none.
func (t *RemoteTrack) Subscribe(ctx context.Context, opts ...SubscribeOption) (*Stream, error) {
	options := SubscribeOptions{BufferSize: defaultBufferSize}
	for _, opt := range opts {
		opt(&options)
	}
	if options.BufferSize < 1 {
		t.manager.params.Logger.Warnw("zero is not a valid buffer size, using one", nil)
		options.BufferSize = 1
	}

	_, hasDeadline := ctx.Deadline()
	ctx, cancel := withDefaultTimeout(ctx, subscribeTimeout)
	defer cancel()

	t.mu.Lock()
	if t.unpublished.IsBroken() {
		t.mu.Unlock()
		return nil, ErrUnpublished
	}
	if t.subscription == subscriptionActive {
		stream := t.addStreamLocked(options.BufferSize)
		t.mu.Unlock()
		return stream, nil
	}
	waiter := make(chan subscribeResult, 1)
	t.waiters = append(t.waiters, waiter)
	var request *subscriptionUpdate
	if t.subscription == subscriptionNone {
		t.subscription = subscriptionPending
		t.bufferSize = options.BufferSize
		request = &subscriptionUpdate{sid: t.info.SID, subscribe: true}
	}
	t.mu.Unlock()

	if request != nil {
		t.manager.sendSubscriptionUpdate(*request)
	}

	select {
	case res := <-waiter:
		return res.stream, res.err
	case <-ctx.Done():
		var withdraw *subscriptionUpdate
		t.mu.Lock()
		t.removeWaiterLocked(waiter)
		if t.subscription == subscriptionPending && len(t.waiters) == 0 {
			t.subscription = subscriptionNone
			withdraw = &subscriptionUpdate{sid: t.info.SID, subscribe: false}
		}
		t.mu.Unlock()

		if withdraw != nil {
			t.manager.sendSubscriptionUpdate(*withdraw)
		}
		select {
		case res := <-waiter:
			// the answer came first and was delivered under the lock
			if res.stream != nil {
				res.stream.Close()
			}
		default:
		}
		if !hasDeadline && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrSubscribeTimeout
		}
		return nil, ctx.Err()
	}
}

func (t *RemoteTrack) removeWaiterLocked(waiter chan subscribeResult) {
	for i, w := range t.waiters {
		if w == waiter {
			t.waiters = append(t.waiters[:i], t.waiters[i+1:]...)
			return
		}
	}
}

// reassignSID moves the track to the SID it was republished under after its publisher's full
// reconnect. It reports the SID replaced and whether a subscription must be re-requested, or false
// when the info differs in more than the SID.
func (t *RemoteTrack) reassignSID(info Info) (oldSID SID, subscribed bool, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// other than the SID, the info should not have changed
	if t.info.Name != info.Name || t.info.UsesE2EE != info.UsesE2EE ||
		!schemaEqual(t.info.Schema, info.Schema) || t.info.FrameEncoding != info.FrameEncoding {
		t.manager.params.Logger.Warnw("data track info mismatch, treating as new publication", nil, "sid", t.info.SID)
		return "", false, false
	}
	oldSID = t.info.SID
	t.manager.params.Logger.Debugw("data track SID reassigned", "oldSid", oldSID, "newSid", info.SID)
	t.info.SID = info.SID
	return oldSID, t.subscription != subscriptionNone, true
}

// subscribedSID reports the track's SID when it has a pending or active subscription.
func (t *RemoteTrack) subscribedSID() (SID, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.info.SID, t.subscription != subscriptionNone
}

// usesHandle reports whether the track's active subscription is routed by handle.
func (t *RemoteTrack) usesHandle(handle trackHandle) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.subscription == subscriptionActive && t.subHandle == handle
}

// activate records the handle the SFU assigned to the subscription. A pending subscription
// becomes active and every waiter receives a stream; an active one takes the new handle, as
// happens after a full reconnect, and the handle it replaces is returned. It reports false when
// the track has no subscription.
func (t *RemoteTrack) activate(handle trackHandle) (previous trackHandle, replaced bool, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.subscription {
	case subscriptionNone:
		return 0, false, false
	case subscriptionActive:
		previous, t.subHandle = t.subHandle, handle
		return previous, true, true
	}

	var decryptor Decryptor
	if t.info.UsesE2EE {
		decryptor = t.manager.params.Decryptor
	}
	t.packets = make(chan *dtp.Packet, packetBufferCount)
	go t.runPipeline(t.packets, newRemotePipeline(decryptor, t.manager.params.Logger))
	t.subHandle = handle
	t.subscription = subscriptionActive
	for _, waiter := range t.waiters {
		waiter <- subscribeResult{stream: t.addStreamLocked(t.bufferSize)}
	}
	t.waiters = nil
	return 0, false, true
}

// deliver hands a packet to the subscription's worker. The packet is dropped when the subscription
// ended, or moved to another handle, since the manager looked the track up.
func (t *RemoteTrack) deliver(handle trackHandle, packet *dtp.Packet) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.subscription != subscriptionActive || t.subHandle != handle {
		t.manager.params.Logger.Debugw("dropping data track packet without subscription", "handle", packet.Handle)
		return
	}
	// the send happens under the lock so the channel is never closed underneath it
	select {
	case t.packets <- packet:
	default:
		t.manager.params.Logger.Debugw("dropping data track packet, pipeline is behind", "sid", t.info.SID)
	}
}

// runPipeline reassembles the subscription's packets on its own goroutine, so tracks never wait on
// one another, and delivers completed frames to every stream. It ends when packets is closed.
func (t *RemoteTrack) runPipeline(packets <-chan *dtp.Packet, pipeline *remotePipeline) {
	for packet := range packets {
		frame, ok := pipeline.processPacket(packet, int(t.maxPartialFrames.Load()))
		if !ok {
			continue
		}
		for _, stream := range *t.streamList.Load() {
			stream.push(frame)
		}
	}
}

// deactivateLocked stops the worker of the active subscription.
func (t *RemoteTrack) deactivateLocked() {
	if t.packets != nil {
		close(t.packets)
		t.packets = nil
	}
}

func (t *RemoteTrack) refreshStreamListLocked() {
	list := make([]*Stream, 0, len(t.streams))
	for stream := range t.streams {
		list = append(list, stream)
	}
	t.streamList.Store(&list)
}

func (t *RemoteTrack) addStreamLocked(bufferSize int) *Stream {
	stream := &Stream{track: t, frames: make(chan Frame, bufferSize)}
	t.streams[stream] = struct{}{}
	t.refreshStreamListLocked()
	return stream
}

// end marks the track unpublished, fails waiters with err, and closes every stream. It reports the
// handle of the active subscription it ended, if there was one.
func (t *RemoteTrack) end(err error) (handle trackHandle, wasActive bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	handle, wasActive = t.subHandle, t.subscription == subscriptionActive
	t.unpublished.Break()
	for _, waiter := range t.waiters {
		waiter <- subscribeResult{err: err}
	}
	t.waiters = nil
	for stream := range t.streams {
		stream.close()
	}
	clear(t.streams)
	t.refreshStreamListLocked()
	t.subscription = subscriptionNone
	t.deactivateLocked()
	return handle, wasActive
}

// removeStream is the subscriber side of Stream.Close: the last stream to leave ends the SFU
// subscription.
func (t *RemoteTrack) removeStream(stream *Stream) {
	var (
		ended  bool
		handle trackHandle
		sid    SID
	)
	t.mu.Lock()
	stream.close()
	if _, present := t.streams[stream]; present {
		delete(t.streams, stream)
		t.refreshStreamListLocked()
		if len(t.streams) == 0 && t.subscription == subscriptionActive {
			t.subscription = subscriptionNone
			t.deactivateLocked()
			ended, handle, sid = true, t.subHandle, t.info.SID
		}
	}
	t.mu.Unlock()

	if ended {
		t.manager.releaseHandle(t, handle)
		t.manager.sendSubscriptionUpdate(subscriptionUpdate{sid: sid, subscribe: false})
	}
}

// Stream delivers the frames of one subscription.
type Stream struct {
	track  *RemoteTrack
	mu     sync.Mutex
	frames chan Frame
	closed bool
}

// Frames yields frames as they arrive. The channel is closed when the stream is closed, the track
// is unpublished, or the room disconnects.
func (s *Stream) Frames() <-chan Frame {
	return s.frames
}

// Close ends the subscription. It is safe to call more than once.
func (s *Stream) Close() {
	s.track.removeStream(s)
}

// push delivers a frame without blocking, dropping the oldest buffered frame when full.
func (s *Stream) push(frame Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.frames <- frame:
		return
	default:
	}
	select {
	case <-s.frames:
	default:
	}
	select {
	case s.frames <- frame:
	default:
	}
}

func (s *Stream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.frames)
	}
}

// remotePipeline turns a subscription's packets back into frames. It is owned by one goroutine.
type remotePipeline struct {
	decryptor    Decryptor
	log          logger.Logger
	depacketizer *depacketizer
}

func newRemotePipeline(decryptor Decryptor, log logger.Logger) *remotePipeline {
	return &remotePipeline{decryptor: decryptor, log: log, depacketizer: newDepacketizer()}
}

// processPacket reports whether the packet completed a frame.
func (p *remotePipeline) processPacket(packet *dtp.Packet, maxPartialFrames int) (Frame, bool) {
	result := p.depacketizer.push(*packet, depacketizerPushOptions{maxPartialFrames: maxPartialFrames})

	if result.drop != nil {
		p.log.Debugw("data track frame dropped", "reason", result.drop.Error())
	}
	if result.frame == nil {
		return Frame{}, false
	}

	frame := Frame{Payload: result.frame.payload, UserTimestamp: result.frame.extensions.UserTimestamp}
	if p.decryptor == nil {
		return frame, true
	}
	e2ee := result.frame.extensions.E2EE
	if e2ee == nil {
		p.log.Errorw("dropping data track frame without E2EE extension", nil)
		return Frame{}, false
	}
	payload, err := p.decryptor.Decrypt(frame.Payload, *e2ee)
	if err != nil {
		p.log.Errorw("dropping data track frame that failed to decrypt", err)
		return Frame{}, false
	}
	frame.Payload = payload
	return frame, true
}

// withDefaultTimeout applies deadline d to ctx when it has none. The returned cancel must be called.
func withDefaultTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
