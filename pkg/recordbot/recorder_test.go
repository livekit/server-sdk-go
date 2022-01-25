package recordbot

import (
	"os"
	"testing"

	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/assert"
)

func createTestCodec() webrtc.RTPCodecParameters {
	// See example params in https://github.com/pion/webrtc/blob/master/examples/save-to-disk/main.go#L53
	return webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeVP8,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "",
			RTCPFeedback: nil,
		},
		PayloadType: 96,
	}
}

func createTestRecorder() recorder {
	codec := createTestCodec()
	writer, _ := createMediaWriter("test.mp4", codec.MimeType)
	done := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)
	return recorder{"test.mp4", writer, done, closed, RecorderHooks{}}
}

func cleanupTestRecorder() {
	os.Remove("test.mp4")
}

/*
 * Test creating webrtc/media.Writer for supported codecs
 */

func TestCanCreateH264Writer(t *testing.T) {
	mimeType := webrtc.MimeTypeH264
	writer, err := createMediaWriter("test.mp4", mimeType)
	assert.NotNil(t, writer)
	assert.NoError(t, err)
	os.Remove("test.mp4")
}

func TestCanCreateVP8Writer(t *testing.T) {
	mimeType := webrtc.MimeTypeVP8
	writer, err := createMediaWriter("test.mp4", mimeType)
	assert.NotNil(t, writer)
	assert.NoError(t, err)
	os.Remove("test.mp4")
}

func TestCanCreateVP9Writer(t *testing.T) {
	mimeType := webrtc.MimeTypeVP9
	writer, err := createMediaWriter("test.mp4", mimeType)
	assert.NotNil(t, writer)
	assert.NoError(t, err)
	os.Remove("test.mp4")
}

func TestCanCreateG722Writer(t *testing.T) {
	mimeType := webrtc.MimeTypeG722
	writer, err := createMediaWriter("test.ogg", mimeType)
	assert.NotNil(t, writer)
	assert.NoError(t, err)
	os.Remove("test.ogg")
}

func TestCanCreatePcmaWriter(t *testing.T) {
	mimeType := webrtc.MimeTypePCMA
	writer, err := createMediaWriter("test.ogg", mimeType)
	assert.NotNil(t, writer)
	assert.NoError(t, err)
	os.Remove("test.ogg")
}

func TestCanCreatePcmuWriter(t *testing.T) {
	mimeType := webrtc.MimeTypePCMU
	writer, err := createMediaWriter("test.ogg", mimeType)
	assert.NotNil(t, writer)
	assert.NoError(t, err)
	os.Remove("test.ogg")
}

func TestCanCreateOpusWriter(t *testing.T) {
	mimeType := webrtc.MimeTypeOpus
	writer, err := createMediaWriter("test.ogg", mimeType)
	assert.NotNil(t, writer)
	assert.NoError(t, err)
	os.Remove("test.ogg")
}

func TestCannotCreateWriterForUnsupportedCodec(t *testing.T) {
	// See https://developer.mozilla.org/en-US/docs/Web/Media/Formats/Video_codecs
	mimeType := "video/H263"
	writer, err := createMediaWriter("test.mp4", mimeType)
	assert.Nil(t, writer)
	assert.ErrorIs(t, err, ErrUnsupportedCodec)
}

/*
 * Test use case
 */

func TestWhenRecorderIsStoppedChannelReceivesStopSignal(t *testing.T) {
	rec := createTestRecorder()
	rec.Stop()
	assert.Equal(t, true, <-rec.done)
	cleanupTestRecorder()
}

func TestCanCreateRecorderForSupportedCodecs(t *testing.T) {
	rec, err := NewSingleTrackRecorder("test.mp4", createTestCodec(), RecorderHooks{})
	assert.NoError(t, err)
	assert.NotNil(t, rec)
	os.Remove("test.mp4")
}

func TestGetNilRecorderForUnsupportedCodec(t *testing.T) {
	unsupportedMimeType := "video/H263"
	rec, err := NewSingleTrackRecorder(
		"test.mp4",
		webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: unsupportedMimeType,
			},
		},
		RecorderHooks{},
	)
	assert.ErrorIs(t, err, ErrUnsupportedCodec)
	assert.Nil(t, rec)
}

/*
 * TODO: Test goroutines
 */
