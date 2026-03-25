package lksdk

import "encoding/binary"

const (
	// packetTrailerMagic must remain consistent with kPacketTrailerMagic
	// in rust-sdks/webrtc-sys/include/livekit/packet_trailer.h.
	packetTrailerMagic = "LKTS"

	// TLV tag IDs (XORed with 0xFF on the wire).
	tagTimestampUs = 0x01 // value: 8 bytes big-endian int64
	tagFrameId     = 0x02 // value: 4 bytes big-endian uint32

	// TLV element sizes: tag(1) + len(1) + value.
	timestampTlvSize = 10 // 1 + 1 + 8
	frameIdTlvSize   = 6  // 1 + 1 + 4

	// Trailer envelope: [trailer_len: 1B XORed] [magic: 4B raw] = 5 bytes.
	trailerEnvelopeSize = 5

	packetTrailerMinSize = timestampTlvSize + trailerEnvelopeSize
	packetTrailerMaxSize = timestampTlvSize + frameIdTlvSize + trailerEnvelopeSize
)

// FrameMetadata holds the metadata embedded in a packet trailer.
type FrameMetadata struct {
	UserTimestampUs int64
	FrameId         uint32
}

// appendPacketTrailer returns a new slice containing data followed by a
// TLV-encoded packet trailer. All TLV bytes are XORed with 0xFF to prevent
// H.264 NAL start-code sequences from appearing inside the trailer.
//
// Wire layout:
//
//	[original data]
//	[TLV: tag=0x01 ^ 0xFF, len=8 ^ 0xFF, 8-byte BE timestamp ^ 0xFF]
//	[TLV: tag=0x02 ^ 0xFF, len=4 ^ 0xFF, 4-byte BE frame_id ^ 0xFF]  (omitted when frameId == 0)
//	[trailer_len ^ 0xFF]
//	[magic "LKTS" raw]
func appendPacketTrailer(data []byte, meta FrameMetadata) []byte {
	hasFrameId := meta.FrameId != 0
	trailerLen := timestampTlvSize + trailerEnvelopeSize
	if hasFrameId {
		trailerLen += frameIdTlvSize
	}

	out := make([]byte, 0, len(data)+trailerLen)
	out = append(out, data...)

	// TLV: timestamp_us
	out = append(out, byte(tagTimestampUs)^0xFF)
	out = append(out, 8^0xFF)
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(meta.UserTimestampUs))
	for _, b := range tsBuf {
		out = append(out, b^0xFF)
	}

	// TLV: frame_id (only when non-zero)
	if hasFrameId {
		out = append(out, byte(tagFrameId)^0xFF)
		out = append(out, 4^0xFF)
		var fidBuf [4]byte
		binary.BigEndian.PutUint32(fidBuf[:], meta.FrameId)
		for _, b := range fidBuf {
			out = append(out, b^0xFF)
		}
	}

	// Envelope: trailer_len (XORed) + magic (raw)
	out = append(out, byte(trailerLen)^0xFF)
	out = append(out, packetTrailerMagic...)

	return out
}

// parsePacketTrailer attempts to parse a packet trailer from the end of the
// provided buffer. It returns the extracted FrameMetadata and true when a
// valid trailer is present.
func parsePacketTrailer(data []byte) (FrameMetadata, bool) {
	if len(data) < trailerEnvelopeSize {
		return FrameMetadata{}, false
	}

	magicStart := len(data) - len(packetTrailerMagic)
	if string(data[magicStart:]) != packetTrailerMagic {
		return FrameMetadata{}, false
	}

	trailerLen := int(data[magicStart-1] ^ 0xFF)
	if trailerLen < trailerEnvelopeSize || trailerLen > len(data) {
		return FrameMetadata{}, false
	}

	tlvStart := len(data) - trailerLen
	tlvRegionLen := trailerLen - trailerEnvelopeSize

	var meta FrameMetadata
	foundAny := false
	pos := 0

	for pos+2 <= tlvRegionLen {
		tag := data[tlvStart+pos] ^ 0xFF
		length := int(data[tlvStart+pos+1] ^ 0xFF)
		pos += 2

		if pos+length > tlvRegionLen {
			break
		}

		valStart := tlvStart + pos

		switch {
		case tag == tagTimestampUs && length == 8:
			var ts uint64
			for i := 0; i < 8; i++ {
				ts = (ts << 8) | uint64(data[valStart+i]^0xFF)
			}
			meta.UserTimestampUs = int64(ts)
			foundAny = true
		case tag == tagFrameId && length == 4:
			var fid uint32
			for i := 0; i < 4; i++ {
				fid = (fid << 8) | uint32(data[valStart+i]^0xFF)
			}
			meta.FrameId = fid
			foundAny = true
		}

		pos += length
	}

	if !foundAny {
		return FrameMetadata{}, false
	}
	return meta, true
}

// stripPacketTrailer returns the data with the packet trailer removed, if
// present. If no valid trailer is found, the original slice is returned.
func stripPacketTrailer(data []byte) []byte {
	if len(data) < trailerEnvelopeSize {
		return data
	}

	magicStart := len(data) - len(packetTrailerMagic)
	if string(data[magicStart:]) != packetTrailerMagic {
		return data
	}

	trailerLen := int(data[magicStart-1] ^ 0xFF)
	if trailerLen < trailerEnvelopeSize || trailerLen > len(data) {
		return data
	}

	return data[:len(data)-trailerLen]
}

// appendUserTimestampTrailer is a backwards-compatible wrapper that appends a
// packet trailer containing only a user timestamp (no frame ID).
func appendUserTimestampTrailer(data []byte, userTimestampUs int64) []byte {
	return appendPacketTrailer(data, FrameMetadata{UserTimestampUs: userTimestampUs})
}

// parseUserTimestampTrailer is a backwards-compatible wrapper that extracts
// the user timestamp from a packet trailer.
func parseUserTimestampTrailer(data []byte) (int64, bool) {
	meta, ok := parsePacketTrailer(data)
	if !ok {
		return 0, false
	}
	return meta.UserTimestampUs, true
}
