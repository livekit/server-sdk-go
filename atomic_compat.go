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
	"math"
	"sync/atomic"
)

// atomicString is a drop-in replacement for go.uber.org/atomic.String.
type atomicString struct {
	v atomic.Pointer[string]
}

func (s *atomicString) Load() string {
	if p := s.v.Load(); p != nil {
		return *p
	}
	return ""
}

func (s *atomicString) Store(val string) {
	s.v.Store(&val)
}

// atomicFloat64 is a drop-in replacement for go.uber.org/atomic.Float64.
type atomicFloat64 struct {
	v atomic.Uint64
}

func (f *atomicFloat64) Load() float64 {
	return math.Float64frombits(f.v.Load())
}

func (f *atomicFloat64) Store(val float64) {
	f.v.Store(math.Float64bits(val))
}
