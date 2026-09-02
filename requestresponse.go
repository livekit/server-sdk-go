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
	"fmt"

	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

// SignalRequestError is returned when the server rejects a signal request.
type SignalRequestError struct {
	RequestID uint32
	Reason    livekit.RequestResponse_Reason
	Message   string
}

func (e *SignalRequestError) Error() string {
	return fmt.Sprintf("signal request rejected (%s): %s", e.Reason, e.Message)
}

// pendingRequest reserves the response to a signal request that carries a request id: create it
// before sending the request, put ID() in the request_id field, then Await the response.
type pendingRequest struct {
	engine *RTCEngine
	id     uint32
	ch     chan proto.Message
}

func (e *RTCEngine) newPendingRequest() *pendingRequest {
	p := &pendingRequest{
		engine: e,
		ch:     make(chan proto.Message, 1),
	}

	e.pendingRequestsLock.Lock()
	p.id = e.allocateRequestIDLocked()
	if e.closed.Load() {
		// nothing will answer, fail Await right away
		close(p.ch)
	} else {
		e.pendingRequests[p.id] = p.ch
	}
	e.pendingRequestsLock.Unlock()

	return p
}

// allocateRequestIDLocked returns the next request id not in use by a pending request, skipping
// zero. Wrapping around is harmless: an id is reused only once its previous request is done.
func (e *RTCEngine) allocateRequestIDLocked() uint32 {
	for {
		e.nextRequestID++
		if e.nextRequestID == 0 {
			continue
		}
		if _, inUse := e.pendingRequests[e.nextRequestID]; !inUse {
			return e.nextRequestID
		}
	}
}

func (p *pendingRequest) ID() uint32 {
	return p.id
}

// Await blocks until a response arrives, ctx is done, or the engine is closed.
//
// A RequestResponse with a reason other than OK is returned as a *SignalRequestError. Any
// other message, including an OK RequestResponse, is returned as is; see awaitResponse to
// also assert its type.
func (p *pendingRequest) Await(ctx context.Context) (proto.Message, error) {
	defer p.engine.removePendingRequest(p.id)

	select {
	case res, ok := <-p.ch:
		if !ok {
			return nil, ErrAborted
		}
		if rr, isRequestResponse := res.(*livekit.RequestResponse); isRequestResponse && rr.GetReason() != livekit.RequestResponse_OK {
			return nil, &SignalRequestError{
				RequestID: rr.GetRequestId(),
				Reason:    rr.GetReason(),
				Message:   rr.GetMessage(),
			}
		}
		return res, nil

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// awaitResponse is Await for requests whose success is reported by a dedicated message type.
func awaitResponse[T proto.Message](ctx context.Context, p *pendingRequest) (T, error) {
	var zero T

	res, err := p.Await(ctx)
	if err != nil {
		return zero, err
	}
	typed, ok := res.(T)
	if !ok {
		return zero, fmt.Errorf("unexpected response %T to signal request %d", res, p.id)
	}
	return typed, nil
}

func (e *RTCEngine) removePendingRequest(requestID uint32) {
	e.pendingRequestsLock.Lock()
	delete(e.pendingRequests, requestID)
	e.pendingRequestsLock.Unlock()
}

// deliverResponse hands res to the request waiting for requestID, if any.
func (e *RTCEngine) deliverResponse(requestID uint32, res proto.Message) bool {
	if requestID == 0 {
		return false
	}

	e.pendingRequestsLock.Lock()
	ch, ok := e.pendingRequests[requestID]
	delete(e.pendingRequests, requestID)
	e.pendingRequestsLock.Unlock()
	if !ok {
		return false
	}

	// buffered, and removed from the map above so this is the only send
	ch <- res
	return true
}

// abortPendingRequests fails every request still waiting for a response.
func (e *RTCEngine) abortPendingRequests() {
	e.pendingRequestsLock.Lock()
	pending := e.pendingRequests
	e.pendingRequests = make(map[uint32]chan proto.Message)
	e.pendingRequestsLock.Unlock()

	for _, ch := range pending {
		close(ch)
	}
}
