package recordbot

import "github.com/pion/webrtc/v3"

func getFileExtension(codecType webrtc.RTPCodecType) string {
	switch codecType {
	case webrtc.RTPCodecTypeVideo:
		return "mp4"
	case webrtc.RTPCodecTypeAudio:
		return "ogg"
	default:
		return ""
	}
}
