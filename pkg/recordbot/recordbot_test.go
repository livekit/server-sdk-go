package recordbot

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/assert"
)

func TestSucceedGenerateRecorderID(t *testing.T) {
	// `id` should have the format `$identity/$codecType`
	id, _ := generateRecorderID("test", webrtc.RTPCodecTypeVideo)

	// Split the ID into tokens using `/` as delimiter
	tokens := strings.Split(id, "/")

	// Extract variables for convenience
	identity := tokens[0]
	codecType := tokens[len(tokens)-1]

	// Assert the tokens match what we passed
	assert.Equal(t, "test", identity)
	assert.Equal(t, webrtc.RTPCodecTypeVideo.String(), codecType)
}

func TestErrorRecorderIDWhenIdentityIsEmpty(t *testing.T) {
	_, err := generateRecorderID("", webrtc.RTPCodecTypeVideo)
	assert.ErrorIs(t, err, ErrIdentityCannotBeEmpty)
}

func TestErrorRecorderIDWhenCodecNotSupported(t *testing.T) {
	// Since webrtc.RTPCodecType is an int, just put in random int to test invalid codec type
	var codecType webrtc.RTPCodecType = 3
	_, err := generateRecorderID("test", codecType)
	assert.ErrorIs(t, err, ErrUnsupportedCodec)
}
