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
	"fmt"

	"github.com/livekit/protocol/livekit"
)

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
		frameEncoding := frameEncodingFromProto(msg.GetFrameEncoding())
		info.FrameEncoding = &frameEncoding
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
		msg.Schema = info.Schema.toProto()
	}
	if info.FrameEncoding != nil {
		msg.FrameEncoding = info.FrameEncoding.toProto()
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
