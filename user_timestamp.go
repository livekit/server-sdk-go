package lksdk

import "encoding/binary"

const (
	// userTimestampMagic must remain consistent with kUserTimestampMagic
	// and kUserTimestampTrailerSize in rust-sdks/webrtc-sys/include/livekit/user_timestamp.h.
	userTimestampMagic       = "LKTS"
	userTimestampTrailerSize = 8 + len(userTimestampMagic)
)

// appendUserTimestampTrailer returns a new slice containing data followed by
// a user timestamp trailer:
//   - 8-byte big-endian int64 timestamp in microseconds
//   - 4-byte ASCII magic "LKTS"
func appendUserTimestampTrailer(data []byte, userTimestampUs int64) []byte {
	outLen := len(data) + userTimestampTrailerSize
	out := make([]byte, outLen)
	copy(out, data)

	// Write timestamp (big-endian) just before the magic bytes.
	tsOffset := len(data)
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(userTimestampUs))
	copy(out[tsOffset:tsOffset+8], tsBuf[:])

	// Append magic bytes.
	copy(out[tsOffset+8:], userTimestampMagic)

	return out
}

// parseUserTimestampTrailer attempts to parse an LKTS trailer from the end
// of the provided buffer. It returns the timestamp in microseconds and true
// when a valid trailer is present.
func parseUserTimestampTrailer(data []byte) (int64, bool) {
	if len(data) < userTimestampTrailerSize {
		return 0, false
	}

	// Check magic bytes at the very end.
	magicStart := len(data) - len(userTimestampMagic)
	if string(data[magicStart:]) != userTimestampMagic {
		return 0, false
	}

	// Timestamp is placed immediately before the magic.
	tsStart := magicStart - 8
	if tsStart < 0 {
		return 0, false
	}

	ts := int64(binary.BigEndian.Uint64(data[tsStart : tsStart+8]))
	return ts, true
}


