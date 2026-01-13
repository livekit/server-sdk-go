package lksdk

import (
	"encoding/binary"
	"testing"
)

func TestParseH264SEIUserTimestamp_UUIDValidation(t *testing.T) {
	const wantTS = int64(1234567890)

	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(wantTS))

	buildNAL := func(uuid [16]byte) []byte {
		// NAL header for SEI (nal_unit_type = 6).
		nal := []byte{0x06}

		// payloadType = 5 (user_data_unregistered)
		// payloadSize = 24 (16-byte UUID + 8-byte timestamp)
		nal = append(nal, 0x05, 0x18)
		nal = append(nal, uuid[:]...)
		nal = append(nal, tsBuf[:]...)
		return nal
	}

	t.Run("accepts matching UUID", func(t *testing.T) {
		gotTS, ok := parseH264SEIUserTimestamp(buildNAL(userTimestampSEIUUID))
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if gotTS != wantTS {
			t.Fatalf("timestamp mismatch: got %d want %d", gotTS, wantTS)
		}
	})

	t.Run("rejects non-matching UUID", func(t *testing.T) {
		badUUID := userTimestampSEIUUID
		badUUID[0] ^= 0xff

		_, ok := parseH264SEIUserTimestamp(buildNAL(badUUID))
		if ok {
			t.Fatalf("expected ok=false")
		}
	})
}
