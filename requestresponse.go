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
	"time"

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
// before sending the request, put ID() in the request_id field, then Await the response. The
// engine owns the registry these are tracked in, see (*RTCEngine).newPendingRequest.
type pendingRequest struct {
	engine *RTCEngine
	id     uint32
	ch     chan proto.Message
}

func (p *pendingRequest) ID() uint32 {
	return p.id
}

// Await blocks until a response arrives, ctx is done, or the engine is closed. It has no timeout
// of its own, see withDefaultTimeout. A RequestResponse with a reason other than OK is returned
// as a *SignalRequestError.
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

// withDefaultTimeout applies deadline d to ctx when it has none. A deadline the caller set is
// never shortened. The returned cancel must be called.
func withDefaultTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
