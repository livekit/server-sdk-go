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
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	dtp "github.com/livekit/protocol/datatrack"
	"github.com/livekit/protocol/utils/pointer"
	"github.com/stretchr/testify/require"
)

// testHeader is the header of the packet used by the Rust serialization tests, without extensions.
func testHeader() dtp.Header {
	header := dtp.Header{
		Version:        supportedVersion,
		Handle:         0x8811,
		SequenceNumber: 0x4422,
		FrameNumber:    0x4411,
		Timestamp:      0x44221188,
	}
	FrameMarkerFinal.apply(&header)
	return header
}

func testExtensions() Extensions {
	var iv [e2eeIVLength]byte
	for i := range iv {
		iv[i] = 0x3c
	}
	return Extensions{
		UserTimestamp: pointer.To(uint64(0x4411221111118811)),
		E2EE:          &E2EEExtension{KeyIndex: 0xfa, IV: iv},
	}
}

// testPacket is the packet used by the Rust serialization tests.
func testPacket() dtp.Packet {
	header := testHeader()
	testExtensions().apply(&header)
	return dtp.Packet{Header: header, Payload: bytes.Repeat([]byte{0xfa}, 1024)}
}

// validRawPacket is the simplest valid raw packet: a base header with a non-zero handle.
func validRawPacket() []byte {
	raw := make([]byte, 12)
	raw[3] = 1
	return raw
}

func withExtensionWords(raw []byte, words uint16) []byte {
	raw[0] |= 1 << 2
	return binary.BigEndian.AppendUint16(raw, words)
}

func TestPacket_Roundtrip(t *testing.T) {
	extensions := testExtensions()
	for _, tc := range []struct {
		name       string
		marker     FrameMarker
		extensions Extensions
	}{
		{"no_extensions", FrameMarkerSingle, Extensions{}},
		{"user_timestamp", FrameMarkerStart, Extensions{UserTimestamp: extensions.UserTimestamp}},
		{"e2ee", FrameMarkerInter, Extensions{E2EE: extensions.E2EE}},
		{"all_extensions", FrameMarkerFinal, extensions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := testHeader()
			tc.marker.apply(&header)
			tc.extensions.apply(&header)
			original := dtp.Packet{Header: header, Payload: bytes.Repeat([]byte{0xab}, 300)}

			raw, err := original.Marshal()
			require.NoError(t, err)
			parsed, err := parsePacket(raw)
			require.NoError(t, err)

			require.Equal(t, original.Header, parsed.Header)
			require.Equal(t, original.Payload, parsed.Payload)
			require.Equal(t, tc.extensions, extensionsOf(&parsed.Header))
		})
	}
}

func TestPacket_Serialize(t *testing.T) {
	packet := testPacket()
	raw, err := packet.Marshal()
	require.NoError(t, err)
	require.Len(t, raw, 1064)

	expectedHeader := []byte{
		0x0c, 0x00, // version 0, final, extensions; reserved
		0x88, 0x11, // handle
		0x44, 0x22, // sequence
		0x44, 0x11, // frame number
		0x44, 0x22, 0x11, 0x88, // timestamp
		0x00, 0x06, // extension words
		0x01, 0x0d, 0xfa, 0x3c, 0x3c, 0x3c, 0x3c, 0x3c, 0x3c, 0x3c, 0x3c, 0x3c, 0x3c, 0x3c, 0x3c, // E2EE
		0x02, 0x08, 0x44, 0x11, 0x22, 0x11, 0x11, 0x11, 0x88, 0x11, // user timestamp
		0x00, // padding
	}
	require.Equal(t, expectedHeader, raw[:len(expectedHeader)])
	require.Equal(t, bytes.Repeat([]byte{0xfa}, 1024), raw[len(expectedHeader):])
}

func TestPacket_UnsupportedVersion(t *testing.T) {
	raw := validRawPacket()
	raw[0] = 0x20
	_, err := parsePacket(raw)
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestPacket_ReservedHandle(t *testing.T) {
	raw := validRawPacket()
	raw[3] = 0
	_, err := parsePacket(raw)
	require.ErrorIs(t, err, ErrInvalidHandle)
}

func TestPacket_ExtE2EE(t *testing.T) {
	raw := withExtensionWords(validRawPacket(), 4)
	raw = append(raw, 1, 13, 0xfa)
	raw = append(raw, bytes.Repeat([]byte{0x3c}, 12)...)
	raw = append(raw, 0, 0, 0)

	packet, err := parsePacket(raw)
	require.NoError(t, err)
	e2ee := extensionsOf(&packet.Header).E2EE
	require.NotNil(t, e2ee)
	require.Equal(t, uint8(0xfa), e2ee.KeyIndex)
	require.Equal(t, bytes.Repeat([]byte{0x3c}, 12), e2ee.IV[:])
}

func TestPacket_ExtUserTimestamp(t *testing.T) {
	raw := withExtensionWords(validRawPacket(), 2)
	raw = append(raw, 2, 8, 0x44, 0x11, 0x22, 0x11, 0x11, 0x11, 0x88, 0x11)

	packet, err := parsePacket(raw)
	require.NoError(t, err)
	require.Equal(t, pointer.To(uint64(0x4411221111118811)), extensionsOf(&packet.Header).UserTimestamp)
}

func TestPacket_ExtForwardCompatLongerLength(t *testing.T) {
	raw := withExtensionWords(validRawPacket(), 3)
	raw = append(raw, 2, 12, 0x44, 0x11, 0x22, 0x11, 0x11, 0x11, 0x88, 0x11) // known 8 bytes
	raw = append(raw, 0xff, 0xff, 0xff, 0xff)                                // extra bytes from a future version

	packet, err := parsePacket(raw)
	require.NoError(t, err)
	require.Equal(t, pointer.To(uint64(0x4411221111118811)), extensionsOf(&packet.Header).UserTimestamp)
}

func TestPacket_ExtShorterThanKnownLengthSkipped(t *testing.T) {
	raw := withExtensionWords(validRawPacket(), 1)
	raw = append(raw, 2, 4, 0x3c, 0x3c, 0x3c, 0x3c)

	packet, err := parsePacket(raw)
	require.NoError(t, err)
	require.Nil(t, extensionsOf(&packet.Header).UserTimestamp)
}

func TestClock_IsBaseAtEpoch(t *testing.T) {
	epoch := time.Now()
	base := timestamp(1234)
	c := newClockWithEpoch(epoch, base)

	require.Equal(t, base, c.at(epoch))
	require.Equal(t, base, c.prev)
}

func TestClock_Monotonic(t *testing.T) {
	epoch := time.Now()
	c := newClockWithEpoch(epoch, 0)

	t1 := epoch.Add(100 * time.Millisecond)
	t0 := epoch.Add(50 * time.Millisecond)
	require.Equal(t, c.at(t1), c.at(t0), "clock went backwards")
}
