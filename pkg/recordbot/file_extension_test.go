package recordbot

import (
	"testing"

	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/assert"
)

func TestGetMp4FileExtensionForVideoCodecType(t *testing.T) {
	extension := getFileExtension(webrtc.RTPCodecTypeVideo)
	assert.Equal(t, "mp4", extension)
}

func TestGetOggFileExtensionForAudioCodecType(t *testing.T) {
	extension := getFileExtension(webrtc.RTPCodecTypeAudio)
	assert.Equal(t, "ogg", extension)
}

func TestGetEmptyExtensionStringForUnsupportedCodecType(t *testing.T) {
	// Since webrtc.RTPCodecType is an int, just put in random int to test invalid codec type
	var codecType webrtc.RTPCodecType = 3
	extension := getFileExtension(codecType)
	assert.Equal(t, "", extension)
}
