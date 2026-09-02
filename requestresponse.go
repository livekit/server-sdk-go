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

	"github.com/livekit/protocol/livekit"
)

// pendingRequest reserves the RequestResponse for a signal request that carries a request id.
//
// Create it with newPendingRequest before sending the request, put ID() in the request's
// request_id field, then Await the response. Once Await returns the reservation is gone and
// a late response is treated as uncorrelated.
type pendingRequest struct {
	engine *RTCEngine
	id     uint32
	ch     chan *livekit.RequestResponse
}

func (e *RTCEngine) newPendingRequest() *pendingRequest {
	p := &pendingRequest{
		engine: e,
		ch:     make(chan *livekit.RequestResponse, 1),
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

// ID is the value to send in the request's request_id field.
func (p *pendingRequest) ID() uint32 {
	return p.id
}

// Await blocks until the response arrives, ctx is done, or the engine is closed.
func (p *pendingRequest) Await(ctx context.Context) (*livekit.RequestResponse, error) {
	defer p.engine.removePendingRequest(p.id)

	select {
	case res, ok := <-p.ch:
		if !ok {
			return nil, ErrAborted
		}
		return res, nil

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *RTCEngine) removePendingRequest(requestID uint32) {
	e.pendingRequestsLock.Lock()
	delete(e.pendingRequests, requestID)
	e.pendingRequestsLock.Unlock()
}

// deliverRequestResponse hands res to the request waiting for its id.
// Returns false if no request is waiting for it.
func (e *RTCEngine) deliverRequestResponse(res *livekit.RequestResponse) bool {
	if res.GetRequestId() == 0 {
		return false
	}

	e.pendingRequestsLock.Lock()
	ch, ok := e.pendingRequests[res.RequestId]
	delete(e.pendingRequests, res.RequestId)
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
	e.pendingRequests = make(map[uint32]chan *livekit.RequestResponse)
	e.pendingRequestsLock.Unlock()

	for _, ch := range pending {
		close(ch)
	}
}
