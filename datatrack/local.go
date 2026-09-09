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
	"sync"
	"sync/atomic"

	dtp "github.com/livekit/protocol/datatrack"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

// transportMTU is the largest packet sent over the data channel.
const transportMTU = 16_000

// LocalTransport carries what the local manager produces: signal requests to the SFU and frame
// packets over the data channel.
type LocalTransport interface {
	SendPublishRequest(req *livekit.PublishDataTrackRequest) error
	SendUnpublishRequest(req *livekit.UnpublishDataTrackRequest) error
	// SendFrame queues all packets of one frame; it must not block.
	SendFrame(packets [][]byte)
}

// Encryptor seals frame payloads for end-to-end encryption.
type Encryptor interface {
	Encrypt(payload []byte) ([]byte, E2EEExtension, error)
}

// PublishOptions describe a data track to publish.
type PublishOptions struct {
	Name          string
	Schema        *SchemaID
	FrameEncoding FrameEncoding
}

// PublishOption customizes a publication.
type PublishOption func(*PublishOptions)

// WithSchema associates a schema with the track's frames.
func WithSchema(schema SchemaID) PublishOption {
	return func(options *PublishOptions) {
		options.Schema = &schema
	}
}

// WithFrameEncoding declares the encoding of the track's frames.
func WithFrameEncoding(encoding FrameEncoding) PublishOption {
	return func(options *PublishOptions) {
		options.FrameEncoding = encoding
	}
}

type LocalManagerParams struct {
	Transport LocalTransport
	// Encryptor returns the encryptor for the current session; nil, or a nil result, disables
	// end-to-end encryption for tracks published from then on.
	Encryptor func() Encryptor
	Logger    logger.Logger
}

// LocalManager tracks the data tracks published by the local participant. Methods are safe for
// concurrent use.
type LocalManager struct {
	params LocalManagerParams

	mu      sync.Mutex
	handles handleAllocator
	pending map[trackHandle]chan publishResult
	active  map[trackHandle]*LocalTrack
}

type publishResult struct {
	track *LocalTrack
	err   error
}

func NewLocalManager(params LocalManagerParams) *LocalManager {
	if params.Logger == nil {
		params.Logger = logger.GetLogger()
	}
	return &LocalManager{
		params:  params,
		pending: make(map[trackHandle]chan publishResult),
		active:  make(map[trackHandle]*LocalTrack),
	}
}

func (m *LocalManager) encryptor() Encryptor {
	if m.params.Encryptor == nil {
		return nil
	}
	return m.params.Encryptor()
}

// Publish requests a publication and waits for the SFU's answer. Ending ctx abandons the request;
// should the SFU accept it afterwards, the track is unpublished right away.
func (m *LocalManager) Publish(ctx context.Context, options PublishOptions) (*LocalTrack, error) {
	var schemaEncoding SchemaEncoding
	if options.Schema != nil {
		schemaEncoding = options.Schema.Encoding
		if schemaEncoding == nil {
			schemaEncoding = SchemaEncodingOther
		}
	}
	if err := validateSchema(options.FrameEncoding, schemaEncoding); err != nil {
		return nil, err
	}

	m.mu.Lock()
	handle, ok := m.handles.get()
	if !ok {
		m.mu.Unlock()
		return nil, ErrLimitReached
	}
	result := make(chan publishResult, 1)
	m.pending[handle] = result
	m.mu.Unlock()

	request := publishRequest{
		handle:        handle,
		name:          options.Name,
		usesE2EE:      m.encryptor() != nil,
		schema:        options.Schema,
		frameEncoding: options.FrameEncoding,
	}
	if err := m.params.Transport.SendPublishRequest(request.toProto()); err != nil {
		m.mu.Lock()
		delete(m.pending, handle)
		m.mu.Unlock()
		return nil, err
	}

	select {
	case res := <-result:
		return res.track, res.err
	case <-ctx.Done():
		m.mu.Lock()
		_, stillPending := m.pending[handle]
		delete(m.pending, handle)
		m.mu.Unlock()
		if !stillPending {
			// the answer came first and was delivered under the lock
			if res := <-result; res.track != nil {
				res.track.Unpublish()
			}
		}
		return nil, ctx.Err()
	}
}

// HandlePublishResponse completes the publication the SFU accepted.
func (m *LocalManager) HandlePublishResponse(msg *livekit.PublishDataTrackResponse) {
	info, err := infoFromProto(msg.GetInfo())
	if err != nil {
		m.params.Logger.Warnw("ignoring invalid publish data track response", err)
		return
	}
	m.resolvePublish(publishResponse{handle: info.pubHandle, info: info})
}

// HandleRequestResponse fails the publication a rejection refers to. It reports whether the
// response was a publish rejection.
func (m *LocalManager) HandleRequestResponse(msg *livekit.RequestResponse) bool {
	rejection, ok := publishRejectionFromRequestResponse(msg)
	if !ok {
		return false
	}
	m.resolvePublish(rejection)
	return true
}

func (m *LocalManager) resolvePublish(response publishResponse) {
	m.mu.Lock()
	if result, ok := m.pending[response.handle]; ok {
		delete(m.pending, response.handle)
		res := publishResult{err: response.err}
		if response.err == nil {
			res.track = newLocalTrack(m, response.info)
			m.active[response.handle] = res.track
		}
		result <- res
		m.mu.Unlock()
		return
	}
	track, isActive := m.active[response.handle]
	m.mu.Unlock()

	switch {
	case isActive && response.err != nil:
		m.params.Logger.Warnw("republish failed for data track", response.err, "handle", response.handle)
	case isActive:
		if track.completeRepublish(response.info.SID) {
			m.params.Logger.Debugw("data track republished", "handle", response.handle, "sid", response.info.SID)
		} else {
			m.params.Logger.Warnw("data track already published", nil, "handle", response.handle)
		}
	case response.err == nil:
		// accepted after the request was abandoned, unpublish to keep the SFU consistent
		m.sendUnpublish(response.handle)
	}
}

// HandleUnpublishResponse completes an unpublication, whether requested locally or by the SFU.
func (m *LocalManager) HandleUnpublishResponse(msg *livekit.UnpublishDataTrackResponse) {
	handle, err := handleFromUint32(msg.GetInfo().GetPubHandle())
	if err != nil {
		m.params.Logger.Warnw("ignoring invalid unpublish data track response", err)
		return
	}

	m.mu.Lock()
	if result, ok := m.pending[handle]; ok {
		delete(m.pending, handle)
		result <- publishResult{err: ErrUnpublished}
	}
	track, ok := m.active[handle]
	delete(m.active, handle)
	m.mu.Unlock()

	if ok {
		track.markUnpublished()
	}
}

func (m *LocalManager) unpublish(handle trackHandle) {
	m.mu.Lock()
	track, ok := m.active[handle]
	delete(m.active, handle)
	m.mu.Unlock()
	if !ok {
		return
	}
	track.markUnpublished()
	m.sendUnpublish(handle)
}

func (m *LocalManager) sendUnpublish(handle trackHandle) {
	if err := m.params.Transport.SendUnpublishRequest(unpublishRequestToProto(handle)); err != nil {
		m.params.Logger.Debugw("could not send unpublish data track request", "handle", handle, "error", err)
	}
}

// RepublishTracks re-requests every publication after a full reconnect. Pending publications fail
// with ErrDisconnected; published tracks drop frames until the SFU answers with their new SID.
func (m *LocalManager) RepublishTracks() {
	m.mu.Lock()
	for handle, result := range m.pending {
		delete(m.pending, handle)
		result <- publishResult{err: ErrDisconnected}
	}
	requests := make([]publishRequest, 0, len(m.active))
	for handle, track := range m.active {
		info := track.beginRepublish()
		requests = append(requests, publishRequest{
			handle:        handle,
			name:          info.Name,
			usesE2EE:      info.UsesE2EE,
			schema:        info.Schema,
			frameEncoding: info.FrameEncoding,
		})
	}
	m.mu.Unlock()

	for _, request := range requests {
		if err := m.params.Transport.SendPublishRequest(request.toProto()); err != nil {
			m.params.Logger.Warnw("could not republish data track", err, "handle", request.handle)
		}
	}
}

// PublishResponsesForSyncState describes the current publications for SyncState.
func (m *LocalManager) PublishResponsesForSyncState() []*livekit.PublishDataTrackResponse {
	m.mu.Lock()
	infos := make([]Info, 0, len(m.active))
	for _, track := range m.active {
		infos = append(infos, track.Info())
	}
	m.mu.Unlock()
	return publishResponsesForSyncState(infos)
}

// Shutdown fails pending publications and unpublishes every track locally. The manager can be
// used again for a new session.
func (m *LocalManager) Shutdown() {
	m.mu.Lock()
	for handle, result := range m.pending {
		delete(m.pending, handle)
		result <- publishResult{err: ErrDisconnected}
	}
	tracks := make([]*LocalTrack, 0, len(m.active))
	for _, track := range m.active {
		tracks = append(tracks, track)
	}
	clear(m.active)
	m.mu.Unlock()

	for _, track := range tracks {
		track.markUnpublished()
	}
}

type publishState int32

const (
	statePublished publishState = iota
	stateRepublishing
	stateUnpublished
)

// LocalTrack is a data track published by the local participant. Methods are safe for concurrent
// use.
type LocalTrack struct {
	manager       *LocalManager
	handle        trackHandle
	state         atomic.Int32
	unpublished   chan struct{}
	unpublishOnce sync.Once

	mu       sync.Mutex
	info     Info
	pipeline localPipeline
}

func newLocalTrack(manager *LocalManager, info Info) *LocalTrack {
	var encryptor Encryptor
	if info.UsesE2EE {
		encryptor = manager.encryptor()
	}
	return &LocalTrack{
		manager:     manager,
		handle:      info.pubHandle,
		unpublished: make(chan struct{}),
		info:        info,
		pipeline:    newLocalPipeline(info.pubHandle, encryptor),
	}
}

// Info returns a snapshot of the track's metadata. The SID changes when the local participant
// completes a full reconnect.
func (t *LocalTrack) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.info
}

// IsPublished reports whether the track is published. A track being republished after a
// reconnect still counts as published.
func (t *LocalTrack) IsPublished() bool {
	return publishState(t.state.Load()) != stateUnpublished
}

// Unpublished is closed once the track is no longer published, whether by Unpublish, the SFU, or
// disconnecting from the room.
func (t *LocalTrack) Unpublished() <-chan struct{} {
	return t.unpublished
}

// TryPush sends a frame to the track's subscribers. It returns ErrUnpublished once the track is
// unpublished and ErrQueueFull while it is being republished after a reconnect. Frames are never
// retained, so the caller may reuse the payload immediately.
func (t *LocalTrack) TryPush(frame Frame) error {
	switch publishState(t.state.Load()) {
	case stateRepublishing:
		return ErrQueueFull
	case stateUnpublished:
		return ErrUnpublished
	}

	t.mu.Lock()
	packets, err := t.pipeline.processFrame(frame)
	t.mu.Unlock()
	if err != nil {
		return err
	}
	t.manager.params.Transport.SendFrame(packets)
	return nil
}

// Unpublish stops the publication. It is safe to call more than once.
func (t *LocalTrack) Unpublish() {
	t.manager.unpublish(t.handle)
}

func (t *LocalTrack) markUnpublished() {
	t.state.Store(int32(stateUnpublished))
	t.unpublishOnce.Do(func() { close(t.unpublished) })
}

func (t *LocalTrack) beginRepublish() Info {
	t.state.Store(int32(stateRepublishing))
	return t.Info()
}

// completeRepublish reports false if the track was not being republished.
func (t *LocalTrack) completeRepublish(sid SID) bool {
	if !t.state.CompareAndSwap(int32(stateRepublishing), int32(statePublished)) {
		return false
	}
	t.mu.Lock()
	t.info.SID = sid
	t.mu.Unlock()
	return true
}

// localPipeline turns frames into packets ready for the data channel.
type localPipeline struct {
	encryptor  Encryptor
	packetizer *packetizer
}

func newLocalPipeline(handle trackHandle, encryptor Encryptor) localPipeline {
	return localPipeline{encryptor: encryptor, packetizer: newPacketizer(handle, transportMTU)}
}

func (p *localPipeline) processFrame(frame Frame) ([][]byte, error) {
	payload := frame.Payload
	extensions := Extensions{UserTimestamp: frame.UserTimestamp}
	if p.encryptor != nil {
		ciphertext, e2ee, err := p.encryptor.Encrypt(payload)
		if err != nil {
			return nil, err
		}
		payload, extensions.E2EE = ciphertext, &e2ee
	}

	packets, err := p.packetizer.packetize(payload, extensions)
	if err != nil {
		return nil, err
	}
	return marshalPackets(packets)
}

func marshalPackets(packets []dtp.Packet) ([][]byte, error) {
	raw := make([][]byte, len(packets))
	for i := range packets {
		buf, err := packets[i].Marshal()
		if err != nil {
			return nil, err
		}
		raw[i] = buf
	}
	return raw, nil
}
