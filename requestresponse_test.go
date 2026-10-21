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

	go engine.OnRequestResponse(&livekit.RequestResponse{RequestId: pending.ID()})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := pending.Await(ctx)
	require.NoError(t, err)
	require.Equal(t, pending.ID(), res.(*livekit.RequestResponse).RequestId)
	require.Zero(t, engine.pendingRequestCount())
}

func TestPendingRequestRejected(t *testing.T) {
	engine := newTestEngine(t)

	pending := engine.newPendingRequest()
	go engine.OnRequestResponse(&livekit.RequestResponse{
		RequestId: pending.ID(),
		Reason:    livekit.RequestResponse_NOT_ALLOWED,
		Message:   "no permission",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := pending.Await(ctx)
	var rejected *SignalRequestError
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, pending.ID(), rejected.RequestID)
	require.Equal(t, livekit.RequestResponse_NOT_ALLOWED, rejected.Reason)
	require.Equal(t, "no permission", rejected.Message)
}

func TestPendingRequestTypedResponse(t *testing.T) {
	engine := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	pending := engine.newPendingRequest()
	go engine.deliverResponse(pending.ID(), &livekit.StoreDataBlobResponse{RequestId: pending.ID()})
	res, err := awaitResponse[*livekit.StoreDataBlobResponse](ctx, pending)
	require.NoError(t, err)
	require.Equal(t, pending.ID(), res.RequestId)

	pending = engine.newPendingRequest()
	go engine.deliverResponse(pending.ID(), &livekit.RequestResponse{RequestId: pending.ID(), Reason: livekit.RequestResponse_NOT_FOUND})
	_, err = awaitResponse[*livekit.GetDataBlobResponse](ctx, pending)
	var rejected *SignalRequestError
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, livekit.RequestResponse_NOT_FOUND, rejected.Reason)

	// a response of the wrong type is an error, not a panic
	pending = engine.newPendingRequest()
	go engine.deliverResponse(pending.ID(), &livekit.RequestResponse{RequestId: pending.ID()})
	_, err = awaitResponse[*livekit.GetDataBlobResponse](ctx, pending)
	require.Error(t, err)
}

func TestPendingRequestContextDone(t *testing.T) {
	engine := newTestEngine(t)

	pending := engine.newPendingRequest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pending.Await(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, engine.pendingRequestCount())

	require.False(t, engine.deliverResponse(pending.ID(), &livekit.RequestResponse{RequestId: pending.ID()}))
}

func TestWithDefaultTimeout(t *testing.T) {
	ctx, cancel := withDefaultTimeout(context.Background(), time.Minute)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(time.Minute), deadline, 5*time.Second)

	// a caller's own deadline is left alone, even when it is longer than the default
	parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
	defer cancelParent()
	parentDeadline, _ := parent.Deadline()
	ctx, cancel = withDefaultTimeout(parent, time.Minute)
	defer cancel()
	deadline, ok = ctx.Deadline()
	require.True(t, ok)
	require.Equal(t, parentDeadline, deadline)

	engine := newTestEngine(t)
	pending := engine.newPendingRequest()
	ctx, cancel = withDefaultTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := pending.Await(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, engine.pendingRequestCount())
}

func TestPendingRequestAbortedOnClose(t *testing.T) {
	engine := newTestEngine(t)

	pending := engine.newPendingRequest()
	engine.Close()
	_, err := pending.Await(context.Background())
	require.ErrorIs(t, err, ErrAborted)

	_, err = engine.newPendingRequest().Await(context.Background())
	require.ErrorIs(t, err, ErrAborted)
	require.Zero(t, engine.pendingRequestCount())
}

func TestUncorrelatedResponseIgnored(t *testing.T) {
	engine := newTestEngine(t)

	// request id 0 is never matched to a pending request
	require.False(t, engine.deliverResponse(0, &livekit.RequestResponse{Reason: livekit.RequestResponse_NOT_ALLOWED}))
	require.False(t, engine.deliverResponse(12345, &livekit.RequestResponse{RequestId: 12345}))
}

func TestRequestIDAllocation(t *testing.T) {
	engine := newTestEngine(t)

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

type requestResponseRecorder struct {
	*Room
	responses chan *livekit.RequestResponse
}

func (r *requestResponseRecorder) OnRequestResponse(res *livekit.RequestResponse) {
	r.responses <- res
}

func TestRequestResponseForwardedToHandler(t *testing.T) {
	recorder := &requestResponseRecorder{
		Room:      NewRoom(&RoomCallback{}),
		responses: make(chan *livekit.RequestResponse, 1),
	}
	t.Cleanup(recorder.Room.engine.Close)
	engine := NewRTCEngine(false, recorder, func() string { return "" }, newRegionURLProvider())
	t.Cleanup(engine.Close)

	// no request id: nothing to correlate, handed to the handler
	uncorrelated := &livekit.RequestResponse{Reason: livekit.RequestResponse_NOT_ALLOWED}
	engine.OnRequestResponse(uncorrelated)
	require.Same(t, uncorrelated, <-recorder.responses)

	// a waiting request consumes its response, the handler is not involved
	pending := engine.newPendingRequest()
	engine.OnRequestResponse(&livekit.RequestResponse{RequestId: pending.ID()})
	_, err := pending.Await(context.Background())
	require.NoError(t, err)
	require.Empty(t, recorder.responses)
}
