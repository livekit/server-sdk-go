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
	"math"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

func newTestEngine(t *testing.T) *RTCEngine {
	engine := NewRoom(&RoomCallback{}).engine
	t.Cleanup(engine.Close)
	return engine
}

func (e *RTCEngine) pendingRequestCount() int {
	e.pendingRequestsLock.Lock()
	defer e.pendingRequestsLock.Unlock()
	return len(e.pendingRequests)
}

func TestPendingRequestDelivered(t *testing.T) {
	engine := newTestEngine(t)

	pending := engine.newPendingRequest()
	require.NotZero(t, pending.ID())
	require.Equal(t, 1, engine.pendingRequestCount())

	go engine.OnRequestResponse(&livekit.RequestResponse{
		RequestId: pending.ID(),
		Reason:    livekit.RequestResponse_NOT_ALLOWED,
		Message:   "no permission",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := pending.Await(ctx)
	require.NoError(t, err)
	require.Equal(t, pending.ID(), res.RequestId)
	require.Equal(t, livekit.RequestResponse_NOT_ALLOWED, res.Reason)
	require.Equal(t, "no permission", res.Message)
	require.Zero(t, engine.pendingRequestCount())
}

func TestPendingRequestContextDone(t *testing.T) {
	engine := newTestEngine(t)

	pending := engine.newPendingRequest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pending.Await(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, engine.pendingRequestCount())

	// a response arriving after the caller gave up is no longer correlated
	require.False(t, engine.deliverRequestResponse(&livekit.RequestResponse{RequestId: pending.ID()}))
}

func TestPendingRequestAbortedOnClose(t *testing.T) {
	engine := newTestEngine(t)

	pending := engine.newPendingRequest()
	engine.Close()
	_, err := pending.Await(context.Background())
	require.ErrorIs(t, err, ErrAborted)

	// requests created after close fail immediately
	_, err = engine.newPendingRequest().Await(context.Background())
	require.ErrorIs(t, err, ErrAborted)
	require.Zero(t, engine.pendingRequestCount())
}

func TestUncorrelatedRequestResponseIgnored(t *testing.T) {
	engine := newTestEngine(t)

	// request id 0 is never matched to a pending request
	require.False(t, engine.deliverRequestResponse(&livekit.RequestResponse{Reason: livekit.RequestResponse_NOT_ALLOWED}))
	// neither is an id nobody is waiting on
	require.False(t, engine.deliverRequestResponse(&livekit.RequestResponse{RequestId: 12345}))
}

func TestRequestIDAllocation(t *testing.T) {
	engine := newTestEngine(t)

	// ids are unique while their requests are pending
	seen := make(map[uint32]struct{})
	for range 100 {
		id := engine.newPendingRequest().ID()
		require.NotZero(t, id)
		_, dup := seen[id]
		require.False(t, dup)
		seen[id] = struct{}{}
	}

	// the counter wraps around without handing out zero or an id that is still in use
	engine.pendingRequestsLock.Lock()
	engine.nextRequestID = math.MaxUint32 - 1
	engine.pendingRequestsLock.Unlock()
	require.Equal(t, uint32(math.MaxUint32), engine.newPendingRequest().ID())
	require.Equal(t, uint32(101), engine.newPendingRequest().ID())
}
