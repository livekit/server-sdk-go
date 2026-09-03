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
	"encoding/binary"
	"errors"
	"fmt"

	dtp "github.com/livekit/protocol/datatrack"
)

const (
	supportedVersion = 0

	extensionIDE2EE          uint8 = 1
	extensionIDUserTimestamp uint8 = 2

	e2eeIVLength                 = 12
	e2eeExtensionLength          = 1 + e2eeIVLength
	userTimestampExtensionLength = 8
)

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

// E2EEExtension carries what is needed to decrypt an end-to-end encrypted payload.
type E2EEExtension struct {
	KeyIndex uint8
	IV       [e2eeIVLength]byte
}

// Extensions are the header extensions understood by this SDK.
type Extensions struct {
	UserTimestamp *uint64
	E2EE          *E2EEExtension
}

func (e Extensions) apply(h *dtp.Header) {
	if e.E2EE != nil {
		data := make([]byte, e2eeExtensionLength)
		data[0] = e.E2EE.KeyIndex
		copy(data[1:], e.E2EE.IV[:])
		h.AddExtension(dtp.NewExtension(extensionIDE2EE, data))
	}
	if e.UserTimestamp != nil {
		data := make([]byte, userTimestampExtensionLength)
		binary.BigEndian.PutUint64(data, *e.UserTimestamp)
		h.AddExtension(dtp.NewExtension(extensionIDUserTimestamp, data))
	}
}

// extensionsOf reads the known extensions. Unknown ids and known ids with less than the
// expected data are skipped; extra data is ignored so a newer version of an extension
// remains readable.
func extensionsOf(h *dtp.Header) Extensions {
	var extensions Extensions
	for _, ext := range h.Extensions {
		data := ext.Data()
		switch {
		case ext.ID() == extensionIDE2EE && len(data) >= e2eeExtensionLength:
			e2ee := &E2EEExtension{KeyIndex: data[0]}
			copy(e2ee.IV[:], data[1:e2eeExtensionLength])
			extensions.E2EE = e2ee
		case ext.ID() == extensionIDUserTimestamp && len(data) >= userTimestampExtensionLength:
			timestamp := binary.BigEndian.Uint64(data)
			extensions.UserTimestamp = &timestamp
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
