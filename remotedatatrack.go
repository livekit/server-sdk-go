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
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/server-sdk-go/v2/datatrack"
)

// remoteDataTrackTransport connects the remote data track manager to the engine and the room's
// callbacks.
type remoteDataTrackTransport struct {
	room *Room
}

func (t remoteDataTrackTransport) SendUpdateSubscription(req *livekit.UpdateDataSubscription) error {
	return t.room.engine.SendUpdateDataSubscription(req)
}

// OnTrackPublished runs the callbacks on their own goroutine so they may block, for example on
// Subscribe, without stalling signal handling.
func (t remoteDataTrackTransport) OnTrackPublished(track *datatrack.RemoteTrack) {
	rp := t.room.GetParticipantByIdentity(track.PublisherIdentity())
	go func() {
		if rp != nil {
			rp.Callback.OnDataTrackPublished(track, rp)
		}
		t.room.callback.OnDataTrackPublished(track, rp)
	}()
}

func (t remoteDataTrackTransport) OnTrackUnpublished(track *datatrack.RemoteTrack) {
	rp := t.room.GetParticipantByIdentity(track.PublisherIdentity())
	go func() {
		if rp != nil {
			rp.Callback.OnDataTrackUnpublished(track, rp)
		}
		t.room.callback.OnDataTrackUnpublished(track, rp)
	}()
}
