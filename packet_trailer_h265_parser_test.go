package lksdk

import (
	"testing"
)

func TestParseH265SEIPacketTrailer(t *testing.T) {
	wantMeta := FrameMetadata{UserTimestampUs: 1234567890}

	buildNAL := func(uuid [16]byte, meta FrameMetadata) []byte {
		// 2-byte NAL header for prefix SEI (nal_unit_type = 39).
		nal := []byte{0x4e, 0x01}

		// Build user_data payload: UUID + LKTS trailer.
		trailer := appendPacketTrailer(nil, meta)
		userData := append(uuid[:], trailer...)

		// payloadType = 5, payloadSize = len(userData)
		nal = append(nal, 0x05, byte(len(userData)))
		nal = append(nal, userData...)
		return nal
	}

	t.Run("accepts matching UUID with timestamp only", func(t *testing.T) {
		got, ok := parseH265SEIPacketTrailer(buildNAL(packetTrailerSEIUUID, wantMeta))
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if got.UserTimestampUs != wantMeta.UserTimestampUs {
			t.Fatalf("timestamp mismatch: got %d want %d", got.UserTimestampUs, wantMeta.UserTimestampUs)
		}
		if got.FrameId != 0 {
			t.Fatalf("expected frame_id 0, got %d", got.FrameId)
		}
	})

	t.Run("accepts matching UUID with timestamp and frame_id", func(t *testing.T) {
		meta := FrameMetadata{UserTimestampUs: 42, FrameId: 12345}
		got, ok := parseH265SEIPacketTrailer(buildNAL(packetTrailerSEIUUID, meta))
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if got.UserTimestampUs != 42 {
			t.Fatalf("timestamp mismatch: got %d want 42", got.UserTimestampUs)
		}
		if got.FrameId != 12345 {
			t.Fatalf("frame_id mismatch: got %d want 12345", got.FrameId)
		}
	})

	t.Run("rejects non-matching UUID", func(t *testing.T) {
		badUUID := packetTrailerSEIUUID
		badUUID[0] ^= 0xFF

		_, ok := parseH265SEIPacketTrailer(buildNAL(badUUID, wantMeta))
		if ok {
			t.Fatalf("expected ok=false for non-matching UUID")
		}
	})

	t.Run("rejects truncated trailer", func(t *testing.T) {
		nal := buildNAL(packetTrailerSEIUUID, wantMeta)
		nal = nal[:len(nal)-3]
		_, ok := parseH265SEIPacketTrailer(nal)
		if ok {
			t.Fatalf("expected ok=false for truncated trailer")
		}
	})

	t.Run("rejects wrong payloadType", func(t *testing.T) {
		nal := []byte{0x4e, 0x01, 0x04, 0x18} // payloadType=4, payloadSize=24
		nal = append(nal, make([]byte, 24)...)
		_, ok := parseH265SEIPacketTrailer(nal)
		if ok {
			t.Fatalf("expected ok=false for wrong payloadType")
		}
	})

	t.Run("rejects nal too short", func(t *testing.T) {
		_, ok := parseH265SEIPacketTrailer([]byte{0x4e, 0x01})
		if ok {
			t.Fatalf("expected ok=false for short NAL")
		}
	})
}
