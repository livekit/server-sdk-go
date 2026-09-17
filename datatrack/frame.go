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

import "time"

// Frame is a frame published on a data track.
type Frame struct {
	Payload []byte
	// UserTimestamp is an optional application-defined timestamp; nil when unset.
	UserTimestamp *uint64
}

// UserTimestampNow returns the current Unix time in milliseconds, the interpretation
// DurationSinceTimestamp expects.
func UserTimestampNow() *uint64 {
	timestamp := uint64(time.Now().UnixMilli())
	return &timestamp
}

// DurationSinceTimestamp returns how much time has passed since the frame's user timestamp,
// taken as Unix milliseconds. It is false if the frame has no user timestamp or the timestamp
// lies in the future.
func (f Frame) DurationSinceTimestamp() (time.Duration, bool) {
	if f.UserTimestamp == nil || *f.UserTimestamp > uint64(1<<63-1) {
		return 0, false
	}
	elapsed := time.Since(time.UnixMilli(int64(*f.UserTimestamp)))
	if elapsed < 0 {
		return 0, false
	}
	return elapsed, true
}
