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
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

func testInfo() Info {
	return Info{SID: "DTR_1234", pubHandle: 1, Name: "track"}
}

func TestProto_InfoFromProto(t *testing.T) {
	response := &livekit.PublishDataTrackResponse{
		Info: &livekit.DataTrackInfo{
			PubHandle:     1,
			Sid:           "DTR_1234",
			Name:          "track",
			Encryption:    livekit.Encryption_GCM,
			Schema:        schemaIDToProto(SchemaID{Name: "schema", Encoding: SchemaEncodingJSONSchema}),
			FrameEncoding: frameEncodingToProto(FrameEncodingJSON),
		},
	}

	info, err := infoFromProto(response.Info)
	require.NoError(t, err)
	require.Equal(t, trackHandle(1), info.pubHandle)
	require.Equal(t, SID("DTR_1234"), info.SID)
	require.Equal(t, "track", info.Name)
	require.Equal(t, &SchemaID{Name: "schema", Encoding: SchemaEncodingJSONSchema}, info.Schema)
	require.Equal(t, FrameEncodingJSON, info.FrameEncoding)
	require.True(t, info.UsesE2EE)
}

func TestProto_FrameEncodingMapping(t *testing.T) {
	base := &livekit.DataTrackInfo{
		PubHandle:  1,
		Sid:        "DTR_1234",
		Name:       "track",
		Encryption: livekit.Encryption_NONE,
	}

	info, err := infoFromProto(base)
	require.NoError(t, err)
	require.Nil(t, info.FrameEncoding)

	unspecified := &livekit.DataTrackInfo{FrameEncoding: frameEncodingToProto(FrameEncodingOther)}
	unspecified.PubHandle, unspecified.Sid, unspecified.Name = base.PubHandle, base.Sid, base.Name
	info, err = infoFromProto(unspecified)
	require.NoError(t, err)
	require.Equal(t, FrameEncodingOther, info.FrameEncoding)

	custom := &livekit.DataTrackInfo{FrameEncoding: frameEncodingToProto(CustomFrameEncoding("my_encoding"))}
	custom.PubHandle, custom.Sid, custom.Name = base.PubHandle, base.Sid, base.Name
	info, err = infoFromProto(custom)
	require.NoError(t, err)
	require.Equal(t, CustomFrameEncoding("my_encoding"), info.FrameEncoding)
}

func TestProto_PublishResponsesForSyncState(t *testing.T) {
	first := testInfo()
	first.UsesE2EE = true

	second := testInfo()
	second.UsesE2EE = false

	responses := publishResponsesForSyncState([]Info{first, second})
	require.Equal(t, livekit.Encryption_GCM, responses[0].Info.Encryption)
	require.Equal(t, livekit.Encryption_NONE, responses[1].Info.Encryption)
}

func TestProto_SubscriberHandlesFromProto(t *testing.T) {
	subscriberHandles := &livekit.DataTrackSubscriberHandles{
		SubHandles: map[uint32]*livekit.DataTrackSubscriberHandles_PublishedDataTrack{
			1: {TrackSid: "DTR_1234"},
			2: {TrackSid: "DTR_4567"},
		},
	}

	mapping, err := subscriberHandlesFromProto(subscriberHandles)
	require.NoError(t, err)
	require.Equal(t, SID("DTR_1234"), mapping[1])
	require.Equal(t, SID("DTR_4567"), mapping[2])
}

func TestProto_ExtractTrackInfo(t *testing.T) {
	participant := &livekit.ParticipantInfo{
		DataTracks: []*livekit.DataTrackInfo{{
			PubHandle:  1,
			Sid:        "DTR_1234",
			Name:       "track1",
			Encryption: livekit.Encryption_GCM,
		}},
	}

	infos, err := extractTrackInfo(participant)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	require.Equal(t, trackHandle(1), infos[0].pubHandle)
	require.Equal(t, "track1", infos[0].Name)
	require.Equal(t, SID("DTR_1234"), infos[0].SID)
}

func TestProto_PublishRequestToProto(t *testing.T) {
	request := publishRequest{handle: 1, name: "track", usesE2EE: true}.toProto()
	require.Equal(t, uint32(1), request.GetPubHandle())
	require.Equal(t, "track", request.GetName())
	require.Equal(t, livekit.Encryption_GCM, request.GetEncryption())
}

func TestProto_UnpublishRequestToProto(t *testing.T) {
	request := unpublishRequestToProto(1)
	require.Equal(t, uint32(1), request.GetPubHandle())
}

func TestProto_PublishRejectionFromRequestResponse(t *testing.T) {
	response := &livekit.RequestResponse{
		Request: &livekit.RequestResponse_PublishDataTrack{
			PublishDataTrack: &livekit.PublishDataTrackRequest{PubHandle: 1},
		},
		Reason: livekit.RequestResponse_NOT_ALLOWED,
	}

	rejection, ok := publishRejectionFromRequestResponse(response)
	require.True(t, ok)
	require.Equal(t, trackHandle(1), rejection.handle)
	require.ErrorIs(t, rejection.err, ErrNotAllowed)
}
