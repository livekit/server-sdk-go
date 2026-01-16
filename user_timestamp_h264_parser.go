package lksdk

import (
	"encoding/binary"
	"fmt"
)

// parseH264SEIUserTimestamp parses H264 SEI NAL units (type 6) carrying
// user_data_unregistered messages and returns a timestamp (microseconds) when detected.
//
// Expected payload format (after the NAL header byte):
//
//	payloadType  = 5 (user_data_unregistered)
//	payloadSize  = 24
//	UUID         = 16 bytes
//	timestamp_us = 8 bytes, big-endian
//	trailing     = 0x80 (stop bits + padding)
func parseH264SEIUserTimestamp(nalData []byte) (int64, bool) {
	if len(nalData) < 2 {
		logger.Infow("H264 SEI user_data_unregistered: nal too short", "nal_len", len(nalData))
		return 0, false
	}

	// Skip NAL header (first byte).
	payload := nalData[1:]
	i := 0

	// Parse payloadType (can be extended with 0xFF bytes).
	payloadType := 0
	for i < len(payload) && payload[i] == 0xFF {
		payloadType += 255
		i++
	}
	if i >= len(payload) {
		logger.Infow("H264 SEI user_data_unregistered: payloadType truncated", "payload_len", len(payload))
		return 0, false
	}
	payloadType += int(payload[i])
	i++

	// We only care about user_data_unregistered (type 5).
	if payloadType != 5 {
		return 0, false
	}

	// Parse payloadSize (can be extended with 0xFF bytes).
	payloadSize := 0
	for i < len(payload) && payload[i] == 0xFF {
		payloadSize += 255
		i++
	}
	if i >= len(payload) {
		logger.Infow("H264 SEI user_data_unregistered: payloadSize truncated", "payload_len", len(payload))
		return 0, false
	}
	payloadSize += int(payload[i])
	i++

	if payloadSize < 24 || len(payload) < i+payloadSize {
		// Not enough data for UUID (16) + timestamp (8).
		logger.Infow(
			"H264 SEI user_data_unregistered: insufficient data for UUID + timestamp",
			"payloadSize", payloadSize,
			"payload_len", len(payload),
			"offset", i,
		)
		return 0, false
	}

	userData := payload[i : i+payloadSize]
	uuidBytes := userData[:16]
	tsBytes := userData[16:24]

	timestampUS := binary.BigEndian.Uint64(tsBytes)

	// Format UUID as 8-4-4-4-12 hex segments (for debug logs).
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x",
		uuidBytes[0:4],
		uuidBytes[4:6],
		uuidBytes[6:8],
		uuidBytes[8:10],
		uuidBytes[10:16],
	)

	logger.Debugw("H264 SEI user_data_unregistered parsed", "uuid", uuid, "timestamp_us", timestampUS)

	return int64(timestampUS), true
}
