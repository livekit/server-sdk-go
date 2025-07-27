// Copyright 2023 LiveKit, Inc.
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

package signalling

import "errors"

var (
	ErrUnsupportedSignalling  = errors.New("unsupported signalling protocol")
	ErrUnsupportedContentType = errors.New("unsupported content type")
	ErrUnimplemented          = errors.New("unimplemented")
	ErrURLNotProvided         = errors.New("URL was not provided")
	ErrInvalidMessageType     = errors.New("invalid message type")
	ErrInvalidParameter       = errors.New("invalid parameter")
	ErrCannotDialSignal       = errors.New("could not dial signal connection")
	ErrMessageQueueNotStarted = errors.New("message queue not started")
	ErrMessageQueueFull       = errors.New("message queue is full")
)
