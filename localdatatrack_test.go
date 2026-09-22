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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The room never connects, so the publish request waits on the engine. The caller's deadline must
// end that wait rather than the 15 s connect timeout.
func TestPublishDataTrackDeadlineInterruptsConnectionWait(t *testing.T) {
	room := NewRoom(&RoomCallback{})
	t.Cleanup(room.engine.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := room.LocalParticipant.PublishDataTrack(ctx, "test")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
}
