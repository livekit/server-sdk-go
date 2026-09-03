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

package datatrack

import "errors"

var (
	ErrNotAllowed         = errors.New("data track publishing unauthorized")
	ErrDuplicateName      = errors.New("track name already taken")
	ErrInvalidName        = errors.New("track name invalid")
	ErrLimitReached       = errors.New("data track publication limit reached")
	ErrPublishTimeout     = errors.New("timed out publishing data track")
	ErrSubscribeTimeout   = errors.New("timed out subscribing to data track")
	ErrDisconnected       = errors.New("room disconnected")
	ErrUnpublished        = errors.New("track unpublished")
	ErrQueueFull          = errors.New("queue full")
	ErrEncryptionDisabled = errors.New("track is end-to-end encrypted but data encryption is not enabled")
)
