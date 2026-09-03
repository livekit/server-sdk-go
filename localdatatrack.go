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
	"context"
	"errors"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/server-sdk-go/v2/datatrack"
)

const dataTrackPublishTimeout = 10 * time.Second

// localDataTrackTransport connects the local data track manager to the engine.
type localDataTrackTransport struct {
	engine *RTCEngine
}

func (t localDataTrackTransport) SendPublishRequest(req *livekit.PublishDataTrackRequest) error {
	if err := t.engine.ensurePublisherConnected(true); err != nil {
		return err
	}
	return t.engine.SendPublishDataTrack(req)
}

func (t localDataTrackTransport) SendUnpublishRequest(req *livekit.UnpublishDataTrackRequest) error {
	return t.engine.SendUnpublishDataTrack(req)
}

func (t localDataTrackTransport) SendFrame(packets [][]byte) {
	t.engine.sendDataTrackFrame(packets)
}

// PublishDataTrack publishes a data track and waits for the server to accept it. A 10 second
// deadline applies when ctx has none. The name must be unique among the participant's data tracks.
func (p *LocalParticipant) PublishDataTrack(ctx context.Context, name string, opts ...datatrack.PublishOption) (*datatrack.LocalTrack, error) {
	options := datatrack.PublishOptions{Name: name}
	for _, opt := range opts {
		opt(&options)
	}

	_, hasDeadline := ctx.Deadline()
	ctx, cancel := withDefaultTimeout(ctx, dataTrackPublishTimeout)
	defer cancel()

	track, err := p.dataTracks.Publish(ctx, options)
	if err != nil {
		if !hasDeadline && errors.Is(err, context.DeadlineExceeded) {
			return nil, datatrack.ErrPublishTimeout
		}
		return nil, err
	}
	p.log.Infow("published data track", "name", name, "trackID", track.Info().SID)
	return track, nil
}
