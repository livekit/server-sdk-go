package lksdk

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestAppendParseRoundTrip_TimestampOnly(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	meta := FrameMetadata{UserTimestampUs: 1_700_000_000_000_000}

	result := appendPacketTrailer(payload, meta)

	if !bytes.HasPrefix(result, payload) {
		t.Fatal("original payload not preserved as prefix")
	}
	if !bytes.HasSuffix(result, []byte(packetTrailerMagic)) {
		t.Fatal("magic bytes missing at end")
	}
	if len(result) != len(payload)+packetTrailerMinSize {
		t.Fatalf("expected len %d, got %d", len(payload)+packetTrailerMinSize, len(result))
	}

	got, ok := parsePacketTrailer(result)
	if !ok {
		t.Fatal("parsePacketTrailer returned false")
	}
	if got.UserTimestampUs != meta.UserTimestampUs {
		t.Fatalf("timestamp mismatch: got %d, want %d", got.UserTimestampUs, meta.UserTimestampUs)
	}
	if got.FrameId != 0 {
		t.Fatalf("expected frame_id 0, got %d", got.FrameId)
	}
}

func TestAppendParseRoundTrip_WithFrameId(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03}
	meta := FrameMetadata{UserTimestampUs: 42, FrameId: 12345}

	result := appendPacketTrailer(payload, meta)

	if len(result) != len(payload)+packetTrailerMaxSize {
		t.Fatalf("expected len %d, got %d", len(payload)+packetTrailerMaxSize, len(result))
	}

	got, ok := parsePacketTrailer(result)
	if !ok {
		t.Fatal("parsePacketTrailer returned false")
	}
	if got.UserTimestampUs != 42 {
		t.Fatalf("timestamp mismatch: got %d, want 42", got.UserTimestampUs)
	}
	if got.FrameId != 12345 {
		t.Fatalf("frame_id mismatch: got %d, want 12345", got.FrameId)
	}
}

func TestAppendParseRoundTrip_ZeroTimestamp(t *testing.T) {
	payload := []byte{0xFF, 0xFF}
	meta := FrameMetadata{UserTimestampUs: 0}

	result := appendPacketTrailer(payload, meta)
	got, ok := parsePacketTrailer(result)
	if !ok {
		t.Fatal("parsePacketTrailer returned false for zero timestamp")
	}
	if got.UserTimestampUs != 0 {
		t.Fatalf("expected 0, got %d", got.UserTimestampUs)
	}
}

func TestAppendParseRoundTrip_NegativeTimestamp(t *testing.T) {
	payload := []byte{0xAA}
	meta := FrameMetadata{UserTimestampUs: -999_999}

	result := appendPacketTrailer(payload, meta)
	got, ok := parsePacketTrailer(result)
	if !ok {
		t.Fatal("parsePacketTrailer returned false")
	}
	if got.UserTimestampUs != -999_999 {
		t.Fatalf("timestamp mismatch: got %d, want -999999", got.UserTimestampUs)
	}
}

func TestAppendParseRoundTrip_MaxFrameId(t *testing.T) {
	payload := []byte{0x00}
	meta := FrameMetadata{UserTimestampUs: 1, FrameId: 0xFFFFFFFF}

	result := appendPacketTrailer(payload, meta)
	got, ok := parsePacketTrailer(result)
	if !ok {
		t.Fatal("parsePacketTrailer returned false")
	}
	if got.FrameId != 0xFFFFFFFF {
		t.Fatalf("frame_id mismatch: got %d, want %d", got.FrameId, uint32(0xFFFFFFFF))
	}
}

func TestAppendParseRoundTrip_EmptyPayload(t *testing.T) {
	meta := FrameMetadata{UserTimestampUs: 7, FrameId: 3}

	result := appendPacketTrailer(nil, meta)
	got, ok := parsePacketTrailer(result)
	if !ok {
		t.Fatal("parsePacketTrailer returned false on empty payload")
	}
	if got.UserTimestampUs != 7 || got.FrameId != 3 {
		t.Fatalf("metadata mismatch: got %+v", got)
	}
}

func TestStripPacketTrailer(t *testing.T) {
	payload := []byte{0x10, 0x20, 0x30, 0x40}
	meta := FrameMetadata{UserTimestampUs: 100, FrameId: 200}

	result := appendPacketTrailer(payload, meta)
	stripped := stripPacketTrailer(result)

	if !bytes.Equal(stripped, payload) {
		t.Fatalf("stripped data mismatch: got %x, want %x", stripped, payload)
	}
}

func TestStripPacketTrailer_NoTrailer(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	stripped := stripPacketTrailer(data)
	if !bytes.Equal(stripped, data) {
		t.Fatal("stripPacketTrailer should return original data when no trailer")
	}
}

func TestParsePacketTrailer_NoMagic(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	_, ok := parsePacketTrailer(data)
	if ok {
		t.Fatal("expected false for data without magic")
	}
}

func TestParsePacketTrailer_TooShort(t *testing.T) {
	_, ok := parsePacketTrailer([]byte{0x01})
	if ok {
		t.Fatal("expected false for data shorter than envelope")
	}

	_, ok = parsePacketTrailer(nil)
	if ok {
		t.Fatal("expected false for nil data")
	}
}

func TestParsePacketTrailer_BadTrailerLen(t *testing.T) {
	// Craft a buffer that has magic but an invalid trailer_len (too large).
	buf := []byte{0x00, 0x00, 0x00} // padding
	buf = append(buf, 0xFF^0xFF)     // trailer_len = 0 (below minimum)
	buf = append(buf, packetTrailerMagic...)

	_, ok := parsePacketTrailer(buf)
	if ok {
		t.Fatal("expected false for trailer_len below minimum")
	}
}

func TestNoNALStartCodes(t *testing.T) {
	// Verify the trailer never contains 0x000001 or 0x00000001 sequences,
	// which would confuse H.264/H.265 parsers.
	testCases := []FrameMetadata{
		{UserTimestampUs: 0, FrameId: 0},
		{UserTimestampUs: 0, FrameId: 1},
		{UserTimestampUs: 1, FrameId: 0},
		{UserTimestampUs: 0x00000001, FrameId: 0x00000001},
		{UserTimestampUs: 0x0000000100000001, FrameId: 0},
		{UserTimestampUs: -1},
	}

	for _, meta := range testCases {
		result := appendPacketTrailer(nil, meta)
		for i := 0; i+2 < len(result); i++ {
			if result[i] == 0x00 && result[i+1] == 0x00 {
				if i+2 < len(result) && result[i+2] == 0x01 {
					t.Fatalf("found 3-byte start code at offset %d for meta %+v: %x", i, meta, result)
				}
				if i+3 < len(result) && result[i+2] == 0x00 && result[i+3] == 0x01 {
					t.Fatalf("found 4-byte start code at offset %d for meta %+v: %x", i, meta, result)
				}
			}
		}
	}
}

func TestBackwardsCompatWrappers(t *testing.T) {
	payload := []byte{0xCA, 0xFE}
	ts := int64(9_876_543_210)

	result := appendUserTimestampTrailer(payload, ts)
	gotTS, ok := parseUserTimestampTrailer(result)
	if !ok {
		t.Fatal("parseUserTimestampTrailer returned false")
	}
	if gotTS != ts {
		t.Fatalf("timestamp mismatch: got %d, want %d", gotTS, ts)
	}
}

func TestCrossCompatWithRustAppendTrailer(t *testing.T) {
	// Build a trailer byte-for-byte matching what the C++/Rust AppendTrailer
	// produces, then verify our Go parser can decode it.
	userTs := int64(1_700_000_000_000_000)
	frameId := uint32(42)

	var trailer []byte

	// TLV: timestamp (tag=0x01 ^ 0xFF, len=8 ^ 0xFF, 8-byte BE value ^ 0xFF)
	trailer = append(trailer, 0x01^0xFF)
	trailer = append(trailer, 8^0xFF)
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(userTs))
	for _, b := range tsBuf {
		trailer = append(trailer, b^0xFF)
	}

	// TLV: frame_id (tag=0x02 ^ 0xFF, len=4 ^ 0xFF, 4-byte BE value ^ 0xFF)
	trailer = append(trailer, 0x02^0xFF)
	trailer = append(trailer, 4^0xFF)
	var fidBuf [4]byte
	binary.BigEndian.PutUint32(fidBuf[:], frameId)
	for _, b := range fidBuf {
		trailer = append(trailer, b^0xFF)
	}

	// Envelope: trailer_len ^ 0xFF, then raw magic
	totalTrailerLen := byte(len(trailer) + trailerEnvelopeSize)
	trailer = append(trailer, totalTrailerLen^0xFF)
	trailer = append(trailer, packetTrailerMagic...)

	payload := []byte{0xDE, 0xAD}
	data := append(payload, trailer...)

	got, ok := parsePacketTrailer(data)
	if !ok {
		t.Fatal("parsePacketTrailer failed on hand-crafted Rust-compatible trailer")
	}
	if got.UserTimestampUs != userTs {
		t.Fatalf("timestamp mismatch: got %d, want %d", got.UserTimestampUs, userTs)
	}
	if got.FrameId != frameId {
		t.Fatalf("frame_id mismatch: got %d, want %d", got.FrameId, frameId)
	}

	stripped := stripPacketTrailer(data)
	if !bytes.Equal(stripped, payload) {
		t.Fatalf("stripped mismatch: got %x, want %x", stripped, payload)
	}
}

func TestParseSkipsUnknownTags(t *testing.T) {
	// Build a trailer with an unknown tag (0x99) inserted between
	// the timestamp and frame_id TLVs. The parser should skip it.
	var trailer []byte

	// TLV: timestamp
	trailer = append(trailer, 0x01^0xFF, 8^0xFF)
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], 777)
	for _, b := range tsBuf {
		trailer = append(trailer, b^0xFF)
	}

	// TLV: unknown tag 0x99, len=2, value=0xAA,0xBB
	trailer = append(trailer, 0x99^0xFF, 2^0xFF, 0xAA^0xFF, 0xBB^0xFF)

	// TLV: frame_id
	trailer = append(trailer, 0x02^0xFF, 4^0xFF)
	var fidBuf [4]byte
	binary.BigEndian.PutUint32(fidBuf[:], 55)
	for _, b := range fidBuf {
		trailer = append(trailer, b^0xFF)
	}

	totalLen := byte(len(trailer) + trailerEnvelopeSize)
	trailer = append(trailer, totalLen^0xFF)
	trailer = append(trailer, packetTrailerMagic...)

	data := append([]byte{0x00}, trailer...)

	got, ok := parsePacketTrailer(data)
	if !ok {
		t.Fatal("parsePacketTrailer should succeed with unknown tags")
	}
	if got.UserTimestampUs != 777 {
		t.Fatalf("timestamp mismatch: got %d, want 777", got.UserTimestampUs)
	}
	if got.FrameId != 55 {
		t.Fatalf("frame_id mismatch: got %d, want 55", got.FrameId)
	}
}
