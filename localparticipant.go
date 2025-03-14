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

package lksdk

import (
	"mime"
	"os"
	"path/filepath"

	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
)

const (
	trackPublishTimeout = 10 * time.Second
)

type LocalParticipant struct {
	baseParticipant
	engine                 *RTCEngine
	subscriptionPermission *livekit.SubscriptionPermission
	serverInfo             *livekit.ServerInfo

	rpcPendingAcks      *sync.Map
	rpcPendingResponses *sync.Map
}

func newLocalParticipant(engine *RTCEngine, roomcallback *RoomCallback, serverInfo *livekit.ServerInfo) *LocalParticipant {
	p := &LocalParticipant{
		baseParticipant:     *newBaseParticipant(roomcallback),
		engine:              engine,
		serverInfo:          serverInfo,
		rpcPendingAcks:      &sync.Map{},
		rpcPendingResponses: &sync.Map{},
	}

	engine.OnRpcAck = p.handleIncomingRpcAck
	engine.OnRpcResponse = p.handleIncomingRpcResponse

	return p
}

func (p *LocalParticipant) PublishTrack(track webrtc.TrackLocal, opts *TrackPublicationOptions) (*LocalTrackPublication, error) {
	if opts == nil {
		opts = &TrackPublicationOptions{}
	}
	kind := KindFromRTPType(track.Kind())
	// default sources, since clients generally look for camera/mic
	if opts.Source == livekit.TrackSource_UNKNOWN {
		if kind == TrackKindVideo {
			opts.Source = livekit.TrackSource_CAMERA
		} else if kind == TrackKindAudio {
			opts.Source = livekit.TrackSource_MICROPHONE
		}
	}

	publisher, ok := p.engine.Publisher()
	if !ok {
		return nil, ErrNoPeerConnection
	}

	pubChan := make(chan *livekit.TrackPublishedResponse, 1)
	p.engine.RegisterTrackPublishedListener(track.ID(), pubChan)
	defer p.engine.UnregisterTrackPublishedListener(track.ID())

	pub := NewLocalTrackPublication(kind, track, *opts, p.engine.client)
	pub.onMuteChanged = p.onTrackMuted

	req := &livekit.AddTrackRequest{
		Cid:        track.ID(),
		Name:       opts.Name,
		Source:     opts.Source,
		Type:       kind.ProtoType(),
		Width:      uint32(opts.VideoWidth),
		Height:     uint32(opts.VideoHeight),
		DisableDtx: opts.DisableDTX,
		Stereo:     opts.Stereo,
		Stream:     opts.Stream,
		Encryption: opts.Encryption,
	}
	if kind == TrackKindVideo {
		// single layer
		req.Layers = []*livekit.VideoLayer{
			{
				Quality: livekit.VideoQuality_HIGH,
				Width:   uint32(opts.VideoWidth),
				Height:  uint32(opts.VideoHeight),
			},
		}
	}
	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: req,
		},
	})
	if err != nil {
		return nil, err
	}

	// add transceivers - re-use if possible, AddTrack will try to re-use.
	// NOTE: `AddTrack` technically cannot re-use transceiver if it was ever
	// used to send media, i. e. if it was ever in a `sendrecv` or `sendonly`
	// direction. But, pion does not enforce that based on browser behaviour
	// observed in practice.
	sender, err := publisher.PeerConnection().AddTrack(track)
	if err != nil {
		return nil, err
	}

	// LocalTrack will consume rtcp packets so we don't need to consume again
	_, isSampleTrack := track.(*LocalTrack)
	pub.setSender(sender, !isSampleTrack)

	publisher.Negotiate()

	var pubRes *livekit.TrackPublishedResponse
	select {
	case pubRes = <-pubChan:
		break
	case <-time.After(trackPublishTimeout):
		return nil, ErrTrackPublishTimeout
	}

	pub.updateInfo(pubRes.Track)
	p.addPublication(pub)

	p.Callback.OnLocalTrackPublished(pub, p)
	p.roomCallback.OnLocalTrackPublished(pub, p)

	p.engine.log.Infow("published track", "name", opts.Name, "source", opts.Source.String(), "trackID", pubRes.Track.Sid)
	return pub, nil
}

// PublishSimulcastTrack publishes up to three layers to the server
func (p *LocalParticipant) PublishSimulcastTrack(tracks []*LocalTrack, opts *TrackPublicationOptions) (*LocalTrackPublication, error) {
	if len(tracks) == 0 {
		return nil, nil
	}

	for _, track := range tracks {
		if track.Kind() != webrtc.RTPCodecTypeVideo {
			return nil, ErrUnsupportedSimulcastKind
		}
		if track.videoLayer == nil || track.RID() == "" {
			return nil, ErrInvalidSimulcastTrack
		}
	}

	tracksCopy := make([]*LocalTrack, len(tracks))
	copy(tracksCopy, tracks)

	// tracks should be low to high
	sort.Slice(tracksCopy, func(i, j int) bool {
		return tracksCopy[i].videoLayer.Width < tracksCopy[j].videoLayer.Width
	})

	if opts == nil {
		opts = &TrackPublicationOptions{}
	}
	// default sources, since clients generally look for camera/mic
	if opts.Source == livekit.TrackSource_UNKNOWN {
		opts.Source = livekit.TrackSource_CAMERA
	}

	mainTrack := tracksCopy[len(tracksCopy)-1]

	pubChan := make(chan *livekit.TrackPublishedResponse, 1)
	p.engine.RegisterTrackPublishedListener(mainTrack.ID(), pubChan)
	defer p.engine.UnregisterTrackPublishedListener(mainTrack.ID())

	pub := NewLocalTrackPublication(KindFromRTPType(mainTrack.Kind()), nil, *opts, p.engine.client)
	pub.onMuteChanged = p.onTrackMuted

	var layers []*livekit.VideoLayer
	for _, st := range tracksCopy {
		layers = append(layers, st.videoLayer)
	}
	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: &livekit.AddTrackRequest{
				Cid:    mainTrack.ID(),
				Name:   opts.Name,
				Source: opts.Source,
				Type:   pub.Kind().ProtoType(),
				Width:  mainTrack.videoLayer.Width,
				Height: mainTrack.videoLayer.Height,
				Layers: layers,
				SimulcastCodecs: []*livekit.SimulcastCodec{
					{
						Codec: mainTrack.Codec().MimeType,
						Cid:   mainTrack.ID(),
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	var pubRes *livekit.TrackPublishedResponse
	select {
	case pubRes = <-pubChan:
		break
	case <-time.After(trackPublishTimeout):
		return nil, ErrTrackPublishTimeout
	}

	publisher, ok := p.engine.Publisher()
	if !ok {
		return nil, ErrNoPeerConnection
	}

	// add transceivers
	publishPC := publisher.PeerConnection()
	var transceiver *webrtc.RTPTransceiver
	var sender *webrtc.RTPSender
	for idx, st := range tracksCopy {
		if idx == 0 {
			// add transceivers - re-use if possible, AddTrack will try to re-use.
			// NOTE: `AddTrack` technically cannot re-use transceiver if it was ever
			// used to send media, i. e. if it was ever in a `sendrecv` or `sendonly`
			// direction. But, pion does not enforce that based on browser behaviour
			// observed in practice.
			sender, err = publishPC.AddTrack(st)
			if err != nil {
				return nil, err
			}

			// as there is no way to get transceiver from sender, search
			for _, tr := range publishPC.GetTransceivers() {
				if tr.Sender() == sender {
					transceiver = tr
					break
				}
			}

			pub.setSender(sender, false)
		} else {
			if err = sender.AddEncoding(st); err != nil {
				return nil, err
			}
		}
		pub.addSimulcastTrack(st)
		st.SetTransceiver(transceiver)
	}

	pub.updateInfo(pubRes.Track)
	p.addPublication(pub)

	publisher.Negotiate()

	p.Callback.OnLocalTrackPublished(pub, p)
	p.roomCallback.OnLocalTrackPublished(pub, p)

	p.engine.log.Infow("published simulcast track", "name", opts.Name, "source", opts.Source.String(), "trackID", pubRes.Track.Sid)

	return pub, nil
}

func (p *LocalParticipant) republishTracks() {
	var localPubs []*LocalTrackPublication
	p.tracks.Range(func(key, value interface{}) bool {
		track := value.(*LocalTrackPublication)

		if track.Track() != nil || len(track.simulcastTracks) > 0 {
			localPubs = append(localPubs, track)
		}
		p.tracks.Delete(key)
		p.audioTracks.Delete(key)
		p.videoTracks.Delete(key)

		p.Callback.OnLocalTrackUnpublished(track, p)
		p.roomCallback.OnLocalTrackUnpublished(track, p)
		return true
	})

	for _, pub := range localPubs {
		opt := pub.PublicationOptions()
		if len(pub.simulcastTracks) > 0 {
			var tracks []*LocalTrack
			for _, st := range pub.simulcastTracks {
				tracks = append(tracks, st)
			}
			p.PublishSimulcastTrack(tracks, &opt)
		} else if track := pub.TrackLocal(); track != nil {
			_, err := p.PublishTrack(track, &opt)
			if err != nil {
				p.engine.log.Warnw("could not republish track", err, "track", pub.SID())
			}
		} else {
			p.engine.log.Warnw("could not republish track as no track local found", nil, "track", pub.SID())
		}
	}
}

func (p *LocalParticipant) closeTracks() {
	var localPubs []*LocalTrackPublication
	p.tracks.Range(func(key, value interface{}) bool {
		track := value.(*LocalTrackPublication)
		if track.Track() != nil || len(track.simulcastTracks) > 0 {
			localPubs = append(localPubs, track)
		}
		p.tracks.Delete(key)
		p.audioTracks.Delete(key)
		p.videoTracks.Delete(key)
		return true
	})

	for _, pub := range localPubs {
		pub.CloseTrack()
	}
}

func (p *LocalParticipant) publishData(kind livekit.DataPacket_Kind, dataPacket *livekit.DataPacket) error {
	if err := p.engine.ensurePublisherConnected(true); err != nil {
		return err
	}

	encoded, err := proto.Marshal(dataPacket)
	if err != nil {
		return err
	}

	return p.engine.GetDataChannel(kind).Send(encoded)
}

// PublishData sends custom user data via WebRTC data channel.
//
// By default, the message can be received by all participants in a room,
// see WithDataPublishDestination for choosing specific participants.
//
// Messages are sent via a LOSSY channel by default, see WithDataPublishReliable for sending reliable data.
//
// Deprecated: Use PublishDataPacket with UserData instead.
func (p *LocalParticipant) PublishData(payload []byte, opts ...DataPublishOption) error {
	options := &dataPublishOptions{}
	for _, opt := range opts {
		opt(options)
	}
	return p.PublishDataPacket(UserData(payload), opts...)
}

// PublishDataPacket sends a packet via a WebRTC data channel. UserData can be used for sending custom user data.
//
// By default, the message can be received by all participants in a room,
// see WithDataPublishDestination for choosing specific participants.
//
// Messages are sent via UDP and offer no delivery guarantees, see WithDataPublishReliable for sending data reliably (with retries).
func (p *LocalParticipant) PublishDataPacket(pck DataPacket, opts ...DataPublishOption) error {
	options := &dataPublishOptions{}
	for _, opt := range opts {
		opt(options)
	}
	dataPacket := pck.ToProto()
	if options.Topic != "" {
		if u, ok := dataPacket.Value.(*livekit.DataPacket_User); ok && u.User != nil {
			u.User.Topic = proto.String(options.Topic)
		}
	}

	// This matches the default value of Kind on protobuf level.
	kind := livekit.DataPacket_LOSSY
	if options.Reliable != nil && *options.Reliable {
		kind = livekit.DataPacket_RELIABLE
	}
	//lint:ignore SA1019 backward compatibility
	dataPacket.Kind = kind

	dataPacket.DestinationIdentities = options.DestinationIdentities
	if u, ok := dataPacket.Value.(*livekit.DataPacket_User); ok && u.User != nil {
		//lint:ignore SA1019 backward compatibility
		u.User.DestinationIdentities = options.DestinationIdentities
	}

	return p.publishData(kind, dataPacket)
}

func (p *LocalParticipant) UnpublishTrack(sid string) error {
	obj, loaded := p.tracks.LoadAndDelete(sid)
	if !loaded {
		return ErrCannotFindTrack
	}
	p.audioTracks.Delete(sid)
	p.videoTracks.Delete(sid)

	pub, ok := obj.(*LocalTrackPublication)
	if !ok {
		return nil
	}

	var err error
	if localTrack, ok := pub.track.(webrtc.TrackLocal); ok {
		publisher, ok := p.engine.Publisher()
		if !ok {
			return ErrNoPeerConnection
		}
		for _, sender := range publisher.pc.GetSenders() {
			if sender.Track() == localTrack {
				err = publisher.pc.RemoveTrack(sender)
				break
			}
		}
		publisher.Negotiate()
	}

	pub.CloseTrack()

	p.Callback.OnLocalTrackUnpublished(pub, p)
	p.roomCallback.OnLocalTrackUnpublished(pub, p)

	p.engine.log.Infow("unpublished track", "name", pub.Name(), "trackID", sid)

	return err
}

// GetSubscriberPeerConnection is a power-user API that gives access to the underlying subscriber peer connection
// subscribed tracks are received using this PeerConnection
func (p *LocalParticipant) GetSubscriberPeerConnection() *webrtc.PeerConnection {
	if subscriber, ok := p.engine.Subscriber(); ok {
		return subscriber.PeerConnection()
	}
	return nil
}

// GetPublisherPeerConnection is a power-user API that gives access to the underlying publisher peer connection
// local tracks are published to server via this PeerConnection
func (p *LocalParticipant) GetPublisherPeerConnection() *webrtc.PeerConnection {
	if publisher, ok := p.engine.Publisher(); ok {
		return publisher.PeerConnection()
	}
	return nil
}

// SetName sets the name of the current participant.
// updates will be performed only if the participant has canUpdateOwnMetadata grant
func (p *LocalParticipant) SetName(name string) {
	_ = p.engine.client.SendUpdateParticipantMetadata(&livekit.UpdateParticipantMetadata{
		Name: name,
	})
}

// SetMetadata sets the metadata of the current participant.
// Updates will be performed only if the participant has canUpdateOwnMetadata grant.
func (p *LocalParticipant) SetMetadata(metadata string) {
	_ = p.engine.client.SendUpdateParticipantMetadata(&livekit.UpdateParticipantMetadata{
		Metadata: metadata,
	})
}

// SetAttributes sets the KV attributes of the current participant.
// To remove an attribute, set it to empty value.
// Updates will be performed only if the participant has canUpdateOwnMetadata grant.
func (p *LocalParticipant) SetAttributes(attrs map[string]string) {
	_ = p.engine.client.SendUpdateParticipantMetadata(&livekit.UpdateParticipantMetadata{
		Attributes: attrs,
	})
}

func (p *LocalParticipant) updateInfo(info *livekit.ParticipantInfo) {
	p.baseParticipant.updateInfo(info, p)

	// detect tracks that have been muted remotely, and apply changes
	for _, ti := range info.Tracks {
		pub := p.getLocalPublication(ti.Sid)
		if pub == nil {
			continue
		}
		if pub.IsMuted() != ti.Muted {
			_ = p.engine.client.SendMuteTrack(pub.SID(), pub.IsMuted())
		}
	}
}

func (p *LocalParticipant) getLocalPublication(sid string) *LocalTrackPublication {
	if pub, ok := p.getPublication(sid).(*LocalTrackPublication); ok {
		return pub
	}
	return nil
}

func (p *LocalParticipant) onTrackMuted(pub *LocalTrackPublication, muted bool) {
	if muted {
		p.Callback.OnTrackMuted(pub, p)
		p.roomCallback.OnTrackMuted(pub, p)
	} else {
		p.Callback.OnTrackUnmuted(pub, p)
		p.roomCallback.OnTrackUnmuted(pub, p)
	}
}

// Control who can subscribe to LocalParticipant's published tracks.
//
// By default, all participants can subscribe. This allows fine-grained control over
// who is able to subscribe at a participant and track level.
//
// Note: if access is given at a track-level (i.e. both `AllParticipants` and
// `TrackPermission.AllTracks` are false), any newer published tracks
// will not grant permissions to any participants and will require a subsequent
// permissions update to allow subscription.
func (p *LocalParticipant) SetSubscriptionPermission(sp *livekit.SubscriptionPermission) {
	p.lock.Lock()
	p.subscriptionPermission = proto.Clone(sp).(*livekit.SubscriptionPermission)
	p.updateSubscriptionPermissionLocked()
	p.lock.Unlock()
}

func (p *LocalParticipant) updateSubscriptionPermission() {
	p.lock.RLock()
	defer p.lock.RUnlock()

	p.updateSubscriptionPermissionLocked()
}

func (p *LocalParticipant) updateSubscriptionPermissionLocked() {
	if p.subscriptionPermission == nil {
		return
	}

	err := p.engine.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_SubscriptionPermission{
			SubscriptionPermission: p.subscriptionPermission,
		},
	})
	if err != nil {
		logger.Errorw(
			"could not send subscription permission", err,
			"participant", p.identity,
			"pID", p.sid,
		)
	}
}

func (p *LocalParticipant) handleParticipantDisconnected(identity string) {
	p.rpcPendingAcks.Range(func(key, value interface{}) bool {
		if value.(rpcPendingAckHandler).participantIdentity == identity {
			p.rpcPendingAcks.Delete(key)
		}
		return true
	})

	p.rpcPendingResponses.Range(func(key, value interface{}) bool {
		if value.(rpcPendingResponseHandler).participantIdentity == identity {
			value.(rpcPendingResponseHandler).resolve(nil, rpcErrorFromBuiltInCodes(RpcRecipientDisconnected, nil))
			p.rpcPendingResponses.Delete(key)
		}
		return true
	})
}

func (p *LocalParticipant) handleIncomingRpcAck(requestId string) {
	handler, ok := p.rpcPendingAcks.Load(requestId)
	if !ok {
		p.engine.log.Errorw("Ack received for unexpected RPC request", nil, "requestId", requestId)
	} else {
		handler.(rpcPendingAckHandler).resolve()
		p.rpcPendingAcks.Delete(requestId)
	}
}

func (p *LocalParticipant) handleIncomingRpcResponse(requestId string, payload *string, error *RpcError) {
	handler, ok := p.rpcPendingResponses.Load(requestId)
	if !ok {
		p.engine.log.Errorw("Response received for unexpected RPC request", nil, "requestId", requestId)
	} else {
		handler.(rpcPendingResponseHandler).resolve(payload, error)
		p.rpcPendingResponses.Delete(requestId)
	}
}

// Initiate an RPC call to a remote participant
//   - @param params - For parameters for initiating the RPC call, see PerformRpcParams
//   - @returns A string payload or an error
func (p *LocalParticipant) PerformRpc(params PerformRpcParams) (*string, error) {
	responseTimeout := 10000 * time.Millisecond
	if params.ResponseTimeout != nil {
		responseTimeout = *params.ResponseTimeout
	}

	resultChan := make(chan *string, 1)
	errorChan := make(chan error, 1)

	maxRoundTripLatency := 2000 * time.Millisecond

	go func() {
		if byteLength(params.Payload) > MaxPayloadBytes {
			errorChan <- rpcErrorFromBuiltInCodes(RpcRequestPayloadTooLarge, nil)
			return
		}

		if p.serverInfo != nil && compareVersions(p.serverInfo.Version, "1.8.0") < 0 {
			errorChan <- rpcErrorFromBuiltInCodes(RpcUnsupportedServer, nil)
			return
		}

		id := uuid.New().String()
		p.engine.publishRpcRequest(params.DestinationIdentity, id, params.Method, params.Payload, responseTimeout-maxRoundTripLatency)

		responseTimer := time.AfterFunc(responseTimeout, func() {
			p.rpcPendingResponses.Delete(id)

			select {
			case errorChan <- rpcErrorFromBuiltInCodes(RpcResponseTimeout, nil):
			default:
			}
		})

		ackTimer := time.AfterFunc(maxRoundTripLatency, func() {
			p.rpcPendingAcks.Delete(id)
			p.rpcPendingResponses.Delete(id)
			responseTimer.Stop()

			select {
			case errorChan <- rpcErrorFromBuiltInCodes(RpcConnectionTimeout, nil):
			default:
			}
		})

		p.rpcPendingAcks.Store(id, rpcPendingAckHandler{
			resolve: func() {
				ackTimer.Stop()
			},
			participantIdentity: params.DestinationIdentity,
		})

		p.rpcPendingResponses.Store(id, rpcPendingResponseHandler{
			resolve: func(payload *string, error *RpcError) {
				responseTimer.Stop()
				if _, ok := p.rpcPendingAcks.Load(id); ok {
					p.engine.log.Warnw("RPC response received before ack", nil, "requestId", id)
					p.rpcPendingAcks.Delete(id)
					ackTimer.Stop()
				}

				if error != nil {
					errorChan <- error
				} else {
					if payload != nil {
						resultChan <- payload
					} else {
						emptyStr := ""
						resultChan <- &emptyStr
					}
				}
			},
			participantIdentity: params.DestinationIdentity,
		})
	}()

	select {
	case result := <-resultChan:
		return result, nil
	case err := <-errorChan:
		return nil, err
	}
}

func (p *LocalParticipant) cleanup() {
	p.rpcPendingAcks = &sync.Map{}
	p.rpcPendingResponses = &sync.Map{}
}

func (p *LocalParticipant) StreamText(options StreamTextOptions) *TextStreamWriter {
	if options.StreamId == nil {
		streamId := uuid.New().String()
		options.StreamId = &streamId
	}

	if options.Attributes == nil {
		options.Attributes = make(map[string]string)
	}

	info := TextStreamInfo{
		baseStreamInfo: &baseStreamInfo{
			Id:       *options.StreamId,
			MimeType: "text/plain",
			Topic:    options.Topic,
			// Q: Is this correct?
			Timestamp:  time.Now().Unix(),
			Size:       options.TotalSize,
			Attributes: options.Attributes,
		},
	}

	header := &livekit.DataStream_Header{
		StreamId:    info.Id,
		MimeType:    info.MimeType,
		Topic:       info.Topic,
		Timestamp:   info.Timestamp,
		TotalLength: info.Size,
		ContentHeader: &livekit.DataStream_Header_TextHeader{
			TextHeader: &livekit.DataStream_TextHeader{
				OperationType: livekit.DataStream_CREATE,
			},
		},
	}
	if options.ReplyToStreamId != nil {
		if textHeader, ok := header.ContentHeader.(*livekit.DataStream_Header_TextHeader); ok {
			textHeader.TextHeader.ReplyToStreamId = *options.ReplyToStreamId
		}
	}

	writer := newTextStreamWriter(info, header, p.engine, options.DestinationIdentities, options.OnProgress)
	p.engine.OnClose(writer.onClose)

	return writer
}

func (p *LocalParticipant) SendText(text string, options StreamTextOptions) TextStreamInfo {
	if options.TotalSize == nil {
		textInBytes := []byte(text)
		totalTextLength := uint64(len(textInBytes))
		options.TotalSize = &totalTextLength
	}

	writer := p.StreamText(options)

	go func() {
		writer.Write(text)
		writer.Close()
	}()
	return writer.Info
}

func (p *LocalParticipant) StreamBytes(options StreamBytesOptions) *ByteStreamWriter {
	if options.StreamId == nil {
		streamId := uuid.New().String()
		options.StreamId = &streamId
	}

	if options.Attributes == nil {
		options.Attributes = make(map[string]string)
	}

	info := ByteStreamInfo{
		baseStreamInfo: &baseStreamInfo{
			Id:         *options.StreamId,
			MimeType:   options.MimeType,
			Topic:      options.Topic,
			Timestamp:  time.Now().Unix(),
			Size:       options.TotalSize,
			Attributes: options.Attributes,
		},
	}

	header := &livekit.DataStream_Header{
		StreamId:    info.Id,
		MimeType:    info.MimeType,
		Topic:       info.Topic,
		Timestamp:   info.Timestamp,
		TotalLength: info.Size,
		ContentHeader: &livekit.DataStream_Header_ByteHeader{
			ByteHeader: &livekit.DataStream_ByteHeader{},
		},
	}

	if options.FileName != nil {
		if byteHeader, ok := header.ContentHeader.(*livekit.DataStream_Header_ByteHeader); ok {
			byteHeader.ByteHeader.Name = *options.FileName
		}
		info.Name = options.FileName
	}

	writer := newByteStreamWriter(info, header, p.engine, options.DestinationIdentities, options.OnProgress)
	p.engine.OnClose(writer.onClose)

	return writer
}

func (p *LocalParticipant) SendFile(filePath string, options StreamBytesOptions) (*ByteStreamInfo, error) {
	if options.TotalSize == nil {
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			return nil, err
		}
		totalSize := uint64(fileInfo.Size())
		options.TotalSize = &totalSize
	}

	if options.MimeType == "" {
		mimeType := mime.TypeByExtension(filepath.Ext(filePath))
		options.MimeType = mimeType
	}

	writer := p.StreamBytes(options)

	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	go func() {
		writer.Write(fileBytes)
		writer.Close()
	}()

	return &writer.Info, nil
}
