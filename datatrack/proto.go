// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package datatrack

import (
	"fmt"

	"github.com/livekit/protocol/livekit"
)

var (
	wellKnownSchemaEncodingToProto = map[WellKnownSchemaEncoding]livekit.DataTrackSchemaEncoding_WellKnownSchemaEncoding{
		SchemaEncodingProtobuf:   livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_PROTOBUF,
		SchemaEncodingFlatbuffer: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_FLATBUFFER,
		SchemaEncodingROS1Msg:    livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_ROS1_MSG,
		SchemaEncodingROS2Msg:    livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_ROS2_MSG,
		SchemaEncodingROS2IDL:    livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_ROS2_IDL,
		SchemaEncodingOMGIDL:     livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_OMG_IDL,
		SchemaEncodingJSONSchema: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_JSON_SCHEMA,
	}
	wellKnownSchemaEncodingFromProto = invert(wellKnownSchemaEncodingToProto)

	wellKnownFrameEncodingToProto = map[WellKnownFrameEncoding]livekit.DataTrackFrameEncoding_WellKnownFrameEncoding{
		FrameEncodingROS1:       livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_ROS1,
		FrameEncodingCDR:        livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_CDR,
		FrameEncodingProtobuf:   livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_PROTOBUF,
		FrameEncodingFlatbuffer: livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_FLATBUFFER,
		FrameEncodingCBOR:       livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_CBOR,
		FrameEncodingMsgPack:    livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_MSGPACK,
		FrameEncodingJSON:       livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_JSON,
	}
	wellKnownFrameEncodingFromProto = invert(wellKnownFrameEncodingToProto)
)

func invert[K, V comparable](m map[K]V) map[V]K {
	inverted := make(map[V]K, len(m))
	for k, v := range m {
		inverted[v] = k
	}
	return inverted
}

// schemaEncodingFromProto maps unspecified and unknown well-known values to SchemaEncodingOther.
func schemaEncodingFromProto(msg *livekit.DataTrackSchemaEncoding) SchemaEncoding {
	switch value := msg.GetValue().(type) {
	case *livekit.DataTrackSchemaEncoding_WellKnown:
		if encoding, known := wellKnownSchemaEncodingFromProto[value.WellKnown]; known {
			return encoding
		}
		return SchemaEncodingOther
	case *livekit.DataTrackSchemaEncoding_Custom:
		return CustomSchemaEncoding(value.Custom)
	default:
		return SchemaEncodingOther
	}
}

// schemaEncodingToProto maps SchemaEncodingOther and nil to the unspecified well-known value.
func schemaEncodingToProto(encoding SchemaEncoding) *livekit.DataTrackSchemaEncoding {
	if custom, ok := encoding.(CustomSchemaEncoding); ok {
		return &livekit.DataTrackSchemaEncoding{Value: &livekit.DataTrackSchemaEncoding_Custom{Custom: string(custom)}}
	}
	wellKnown, _ := encoding.(WellKnownSchemaEncoding)
	return &livekit.DataTrackSchemaEncoding{Value: &livekit.DataTrackSchemaEncoding_WellKnown{
		WellKnown: wellKnownSchemaEncodingToProto[wellKnown],
	}}
}

func schemaIDFromProto(msg *livekit.DataTrackSchemaId) SchemaID {
	id := SchemaID{Name: msg.GetName(), Encoding: SchemaEncodingOther}
	if msg.GetEncoding() != nil {
		id.Encoding = schemaEncodingFromProto(msg.GetEncoding())
	}
	return id
}

func schemaIDToProto(id SchemaID) *livekit.DataTrackSchemaId {
	return &livekit.DataTrackSchemaId{Name: id.Name, Encoding: schemaEncodingToProto(id.Encoding)}
}

// frameEncodingFromProto maps unspecified and unknown well-known values to FrameEncodingOther.
func frameEncodingFromProto(msg *livekit.DataTrackFrameEncoding) FrameEncoding {
	switch value := msg.GetValue().(type) {
	case *livekit.DataTrackFrameEncoding_WellKnown:
		if encoding, known := wellKnownFrameEncodingFromProto[value.WellKnown]; known {
			return encoding
		}
		return FrameEncodingOther
	case *livekit.DataTrackFrameEncoding_Custom:
		return CustomFrameEncoding(value.Custom)
	default:
		return FrameEncodingOther
	}
}

// frameEncodingToProto maps FrameEncodingOther and nil to the unspecified well-known value.
func frameEncodingToProto(encoding FrameEncoding) *livekit.DataTrackFrameEncoding {
	if custom, ok := encoding.(CustomFrameEncoding); ok {
		return &livekit.DataTrackFrameEncoding{Value: &livekit.DataTrackFrameEncoding_Custom{Custom: string(custom)}}
	}
	wellKnown, _ := encoding.(WellKnownFrameEncoding)
	return &livekit.DataTrackFrameEncoding{Value: &livekit.DataTrackFrameEncoding_WellKnown{
		WellKnown: wellKnownFrameEncodingToProto[wellKnown],
	}}
}

func infoFromProto(msg *livekit.DataTrackInfo) (Info, error) {
	handle, err := handleFromUint32(msg.GetPubHandle())
	if err != nil {
		return Info{}, err
	}

	var usesE2EE bool
	switch msg.GetEncryption() {
	case livekit.Encryption_NONE:
	case livekit.Encryption_GCM:
		usesE2EE = true
	default:
		return Info{}, fmt.Errorf("unsupported E2EE type: %s", msg.GetEncryption())
	}

	sid, err := parseSID(msg.GetSid())
	if err != nil {
		return Info{}, err
	}

	info := Info{SID: sid, pubHandle: handle, Name: msg.GetName(), UsesE2EE: usesE2EE}
	if msg.GetSchema() != nil {
		schema := schemaIDFromProto(msg.GetSchema())
		info.Schema = &schema
	}
	if msg.GetFrameEncoding() != nil {
		info.FrameEncoding = frameEncodingFromProto(msg.GetFrameEncoding())
	}
	return info, nil
}

func infoToProto(info Info) *livekit.DataTrackInfo {
	msg := &livekit.DataTrackInfo{
		PubHandle:  uint32(info.pubHandle),
		Sid:        string(info.SID),
		Name:       info.Name,
		Encryption: livekit.Encryption_NONE,
	}
	if info.UsesE2EE {
		msg.Encryption = livekit.Encryption_GCM
	}
	if info.Schema != nil {
		msg.Schema = schemaIDToProto(*info.Schema)
	}
	if info.FrameEncoding != nil {
		msg.FrameEncoding = frameEncodingToProto(info.FrameEncoding)
	}
	return msg
}

// publishResponsesForSyncState describes the published tracks for SyncState.
func publishResponsesForSyncState(published []Info) []*livekit.PublishDataTrackResponse {
	responses := make([]*livekit.PublishDataTrackResponse, 0, len(published))
	for _, info := range published {
		responses = append(responses, &livekit.PublishDataTrackResponse{Info: infoToProto(info)})
	}
	return responses
}

// subscriberHandlesFromProto maps the handles of incoming packets to the tracks they belong to.
func subscriberHandlesFromProto(msg *livekit.DataTrackSubscriberHandles) (map[trackHandle]SID, error) {
	mapping := make(map[trackHandle]SID, len(msg.GetSubHandles()))
	for rawHandle, track := range msg.GetSubHandles() {
		handle, err := handleFromUint32(rawHandle)
		if err != nil {
			return nil, err
		}
		sid, err := parseSID(track.GetTrackSid())
		if err != nil {
			return nil, err
		}
		mapping[handle] = sid
	}
	return mapping, nil
}

// extractTrackInfo returns the data tracks published by a participant.
func extractTrackInfo(participant *livekit.ParticipantInfo) ([]Info, error) {
	infos := make([]Info, 0, len(participant.GetDataTracks()))
	for _, msg := range participant.GetDataTracks() {
		info, err := infoFromProto(msg)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// publishRequest is what the local participant asks the SFU to publish.
type publishRequest struct {
	handle        trackHandle
	name          string
	usesE2EE      bool
	schema        *SchemaID
	frameEncoding FrameEncoding
}

func (r publishRequest) toProto() *livekit.PublishDataTrackRequest {
	req := &livekit.PublishDataTrackRequest{
		PubHandle:  uint32(r.handle),
		Name:       r.name,
		Encryption: livekit.Encryption_NONE,
	}
	if r.usesE2EE {
		req.Encryption = livekit.Encryption_GCM
	}
	if r.schema != nil {
		req.Schema = schemaIDToProto(*r.schema)
	}
	if r.frameEncoding != nil {
		req.FrameEncoding = frameEncodingToProto(r.frameEncoding)
	}
	return req
}

func unpublishRequestToProto(handle trackHandle) *livekit.UnpublishDataTrackRequest {
	return &livekit.UnpublishDataTrackRequest{PubHandle: uint32(handle)}
}

// publishResponse is the SFU's answer to a publishRequest: info when accepted, err when rejected.
type publishResponse struct {
	handle trackHandle
	info   Info
	err    error
}

// publishRejectionFromRequestResponse extracts a rejected publish request; ok is false when the
// response is not a publish rejection.
func publishRejectionFromRequestResponse(msg *livekit.RequestResponse) (rejection publishResponse, ok bool) {
	request, isPublish := msg.GetRequest().(*livekit.RequestResponse_PublishDataTrack)
	if !isPublish || msg.GetReason() == livekit.RequestResponse_OK {
		return publishResponse{}, false
	}
	handle, err := handleFromUint32(request.PublishDataTrack.GetPubHandle())
	if err != nil {
		return publishResponse{}, false
	}

	rejection = publishResponse{handle: handle}
	switch msg.GetReason() {
	case livekit.RequestResponse_NOT_ALLOWED:
		rejection.err = ErrNotAllowed
	case livekit.RequestResponse_DUPLICATE_NAME:
		rejection.err = ErrDuplicateName
	case livekit.RequestResponse_INVALID_NAME:
		rejection.err = ErrInvalidName
	case livekit.RequestResponse_LIMIT_EXCEEDED:
		rejection.err = ErrLimitReached
	default:
		rejection.err = fmt.Errorf("data track publish rejected (%s): %s", msg.GetReason(), msg.GetMessage())
		return rejection, true
	}
	if message := msg.GetMessage(); message != "" {
		rejection.err = fmt.Errorf("%w: %s", rejection.err, message)
	}
	return rejection, true
}
