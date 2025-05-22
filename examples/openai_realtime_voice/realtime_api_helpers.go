package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"

	"github.com/livekit/media-sdk"
	"github.com/livekit/protocol/logger"
)

func sendAudioChunk(ws *WsManager, sample media.PCM16Sample) error {
	bytes := make([]byte, len(sample)*2)
	for i, s := range sample {
		binary.LittleEndian.PutUint16(bytes[i*2:], uint16(s))
	}
	encodedSample := base64.StdEncoding.EncodeToString(bytes)
	return ws.SendMessage("input_audio_buffer.append", "audio", encodedSample)
}

func handleMessage(message string, cb *WsManagerCallbacks) {
	var data map[string]interface{}
	err := json.Unmarshal([]byte(message), &data)
	if err != nil {
		logger.Errorw("Failed to unmarshal message", err)
		return
	}

	switch data["type"] {
	case "response.audio.delta":
		audioBase64 := data["delta"].(string)
		audioBytes, err := base64.StdEncoding.DecodeString(audioBase64)
		audioPCM16 := make(media.PCM16Sample, len(audioBytes)/2)
		for i := 0; i < len(audioBytes); i += 2 {
			audioPCM16[i/2] = int16(binary.LittleEndian.Uint16(audioBytes[i : i+2]))
		}
		if err != nil {
			logger.Errorw("Failed to decode audio", err)
			return
		}
		cb.OnAudioReceived(audioPCM16)
	default:
		logger.Errorw("Unknown message type", nil, "type", data["type"])
	}
}
