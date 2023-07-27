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

package jitter

import "github.com/livekit/protocol/logger"

type Option func(b *Buffer)

// WithPacketDroppedHandler sets a callback that's called when a packet
// is dropped. This signifies packet loss.
func WithPacketDroppedHandler(f func()) Option {
	return func(b *Buffer) {
		b.onPacketDropped = f
	}
}

// WithLogger sets a logger which will log packets dropped
func WithLogger(l logger.Logger) Option {
	return func(b *Buffer) {
		b.logger = l
	}
}
