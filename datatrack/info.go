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
	"strings"

	"github.com/livekit/protocol/utils/guid"
)

var ErrInvalidSID = errors.New("invalid data track SID")

// SID is the server-assigned identifier of a data track.
type SID string

func parseSID(raw string) (SID, error) {
	if !strings.HasPrefix(raw, guid.DataTrackPrefix) {
		return "", ErrInvalidSID
	}
	return SID(raw), nil
}

// Info describes a published data track. The SID changes when the publisher completes a
// full reconnect; Name is stable.
type Info struct {
	SID           SID
	pubHandle     trackHandle
	Name          string
	UsesE2EE      bool
	Schema        *SchemaID
	FrameEncoding *FrameEncoding
}
