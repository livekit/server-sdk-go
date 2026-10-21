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
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"

	"github.com/livekit/server-sdk-go/v2/datatrack"
)

// A participant's disconnect removes it from the room before its data tracks are unpublished; the
// unpublish callbacks must still receive it.
func TestDataTrackUnpublishedOnParticipantDisconnect(t *testing.T) {
	roomUnpublished := make(chan *RemoteParticipant, 1)
	room := NewRoom(&RoomCallback{
		ParticipantCallback: ParticipantCallback{
			OnDataTrackUnpublished: func(_ *datatrack.RemoteTrack, rp *RemoteParticipant) {
				roomUnpublished <- rp
			},
		},
	})
	t.Cleanup(room.engine.Close)

	info := &livekit.ParticipantInfo{
		Sid:      "PA_remote",
		Identity: "remote",
		State:    livekit.ParticipantInfo_ACTIVE,
		DataTracks: []*livekit.DataTrackInfo{
			{Sid: "DTR_1234", PubHandle: 1, Name: "test"},
		},
	}
	room.OnParticipantUpdate([]*livekit.ParticipantInfo{info})
	rp := room.GetParticipantByIdentity("remote")
	require.NotNil(t, rp)

	participantUnpublished := make(chan struct{}, 1)
	rp.Callback.OnDataTrackUnpublished = func(*datatrack.RemoteTrack, *RemoteParticipant) {
		participantUnpublished <- struct{}{}
	}

	room.OnParticipantUpdate([]*livekit.ParticipantInfo{{
		Sid:      info.Sid,
		Identity: info.Identity,
		State:    livekit.ParticipantInfo_DISCONNECTED,
	}})

	select {
	case <-participantUnpublished:
	case <-time.After(time.Second):
		t.Fatal("participant callback not called")
	}
	select {
	case got := <-roomUnpublished:
		require.Same(t, rp, got)
	case <-time.After(time.Second):
		t.Fatal("room callback not called")
	}
}
