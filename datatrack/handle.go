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
	"errors"
	"math"
)

var (
	errHandleReserved = errors.New("data track handle 0 is reserved")
	errHandleTooLarge = errors.New("value too large to be a data track handle")
)

// trackHandle identifies the data track a packet belongs to.
type trackHandle uint16

func handleFromUint32(value uint32) (trackHandle, error) {
	if value > math.MaxUint16 {
		return 0, errHandleTooLarge
	}
	if value == 0 {
		return 0, errHandleReserved
	}
	return trackHandle(value), nil
}

// handleAllocator hands out unique handles for local publications. Handles are never reused.
type handleAllocator struct {
	value uint16
}

func (a *handleAllocator) get() (trackHandle, bool) {
	if a.value == math.MaxUint16 {
		return 0, false
	}
	a.value++
	return trackHandle(a.value), true
}
