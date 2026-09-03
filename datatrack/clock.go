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

import (
	"math/rand/v2"
	"time"
)

const timestampRate = 90_000

// timestamp is a packet-level timestamp in ticks of timestampRate.
type timestamp uint32

func randomTimestamp() timestamp {
	return timestamp(rand.Uint32())
}

func (t timestamp) isBefore(other timestamp) bool {
	return int32(t-other) < 0
}

func (t timestamp) wrappingAdd(ticks uint32) timestamp {
	return timestamp(uint32(t) + ticks)
}

// clock maps instants to monotonic packet timestamps.
type clock struct {
	epoch time.Time
	base  timestamp
	prev  timestamp
}

func newClock(base timestamp) *clock {
	return newClockWithEpoch(time.Now(), base)
}

func newClockWithEpoch(epoch time.Time, base timestamp) *clock {
	return &clock{epoch: epoch, base: base, prev: base}
}

func (c *clock) now() timestamp {
	return c.at(time.Now())
}

func (c *clock) at(instant time.Time) timestamp {
	ts := c.base.wrappingAdd(durationToTicks(instant.Sub(c.epoch)))
	if ts.isBefore(c.prev) {
		ts = c.prev
	}
	c.prev = ts
	return ts
}

func durationToTicks(d time.Duration) uint32 {
	nanos := d.Nanoseconds()
	if nanos < 0 {
		nanos = 0
	}
	seconds, remainder := uint64(nanos)/1_000_000_000, uint64(nanos)%1_000_000_000
	return uint32(seconds*timestampRate + (remainder*timestampRate+500_000_000)/1_000_000_000)
}
