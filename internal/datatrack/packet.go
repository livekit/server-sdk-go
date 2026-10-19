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
	"fmt"

	dtp "github.com/livekit/protocol/datatrack"
)

const supportedVersion = 0

var (
	ErrUnsupportedVersion = errors.New("unsupported data track packet version")
	ErrInvalidHandle      = errors.New("invalid data track handle")
)

// FrameMarker is a packet's position within its frame.
type FrameMarker uint8

const (
	FrameMarkerInter FrameMarker = iota
	FrameMarkerStart
	FrameMarkerFinal
	FrameMarkerSingle
)

func markerOf(h *dtp.Header) FrameMarker {
	switch {
	case h.IsStartOfFrame && h.IsFinalOfFrame:
		return FrameMarkerSingle
	case h.IsStartOfFrame:
		return FrameMarkerStart
	case h.IsFinalOfFrame:
		return FrameMarkerFinal
	default:
		return FrameMarkerInter
	}
}

func (m FrameMarker) apply(h *dtp.Header) {
	h.IsStartOfFrame = m == FrameMarkerStart || m == FrameMarkerSingle
	h.IsFinalOfFrame = m == FrameMarkerFinal || m == FrameMarkerSingle
}

// Extensions are the header extensions understood by this SDK.
type Extensions struct {
	UserTimestamp *uint64
	E2EE          *dtp.ExtensionE2EE
}

func (e Extensions) apply(h *dtp.Header) error {
	if e.E2EE != nil {
		ext, err := e.E2EE.Marshal()
		if err != nil {
			return err
		}
		h.AddExtension(ext)
	}
	if e.UserTimestamp != nil {
		ext, err := dtp.NewExtensionUserTimestamp(*e.UserTimestamp).Marshal()
		if err != nil {
			return err
		}
		h.AddExtension(ext)
	}
	return nil
}

// extensionsOf reads the known extensions. Unknown ids and known ids that fail to
// unmarshal (e.g. less than the expected data) are skipped.
func extensionsOf(h *dtp.Header) Extensions {
	var extensions Extensions
	for _, ext := range h.Extensions {
		switch ext.ID() {
		case dtp.ExtensionE2EEID:
			var e2ee dtp.ExtensionE2EE
			if e2ee.Unmarshal(ext) == nil {
				extensions.E2EE = &e2ee
			}
		case dtp.ExtensionUserTimestampID:
			var userTimestamp dtp.ExtensionUserTimestamp
			if userTimestamp.Unmarshal(ext) == nil {
				timestamp := userTimestamp.Timestamp()
				extensions.UserTimestamp = &timestamp
			}
		}
	}
	return extensions
}

func parsePacket(buf []byte) (*dtp.Packet, error) {
	var packet dtp.Packet
	if err := packet.Unmarshal(buf); err != nil {
		return nil, err
	}
	if packet.Version > supportedVersion {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedVersion, packet.Version)
	}
	if packet.Handle == 0 {
		return nil, fmt.Errorf("%w: 0 is reserved", ErrInvalidHandle)
	}
	return &packet, nil
}
