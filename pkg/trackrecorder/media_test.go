package trackrecorder

import (
	"testing"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/stretchr/testify/require"
)

func TestExtensionOpus(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypeOpus)
	require.Equal(t, MediaOGG, ext)
}

func TestExtensionVP8(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypeVP8)
	require.Equal(t, MediaIVF, ext)
}

func TestExtensionH264(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypeH264)
	require.Equal(t, MediaH264, ext)
}

func TestExtensionVP9GetEmptyString(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypeVP9)
	require.Empty(t, ext)
}

func TestExtensionH265GetEmptyString(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypeH265)
	require.Equal(t, MediaExtension(""), ext)
}

func TestExtensionAV1GetEmptyString(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypeAV1)
	require.Equal(t, MediaExtension(""), ext)
}

func TestExtensionG722GetEmptyString(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypeG722)
	require.Equal(t, MediaExtension(""), ext)
}

func TestExtensionPCMUGetEmptyString(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypePCMU)
	require.Equal(t, MediaExtension(""), ext)
}

func TestExtensionPCMAGetEmptyString(t *testing.T) {
	ext := GetMediaExtension(webrtc.MimeTypePCMA)
	require.Equal(t, MediaExtension(""), ext)
}

func TestGetH264Writer(t *testing.T) {
	sink := NewBufferSink("")
	defer sink.Close()
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeH264,
			Channels: 1,
		},
	}
	mw, err := createMediaWriter(sink, codec)
	require.NoError(t, err)
	require.Implements(t, (*media.Writer)(nil), mw)
}

func TestGetIVFWriter(t *testing.T) {
	sink := NewBufferSink("")
	defer sink.Close()
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeVP8,
			Channels: 1,
		},
	}
	mw, err := createMediaWriter(sink, codec)
	require.NoError(t, err)
	require.Implements(t, (*media.Writer)(nil), mw)
}

func TestGetOGGWriter(t *testing.T) {
	sink := NewBufferSink("")
	defer sink.Close()
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus,
			Channels: 2,
		},
	}
	mw, err := createMediaWriter(sink, codec)
	require.NoError(t, err)
	require.Implements(t, (*media.Writer)(nil), mw)
}

func TestGetUnsupportedWriter(t *testing.T) {
	sink := NewBufferSink("")
	defer sink.Close()
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeAV1,
			Channels: 1,
		},
	}
	_, err := createMediaWriter(sink, codec)
	require.ErrorIs(t, ErrCodecNotSupported, err)
}

func TestVP8SampleBuilder(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeVP8,
			Channels: 1,
		},
	}
	sb := createSampleBuilder(codec)
	require.NotNil(t, sb)
}

func TestH264SampleBuilder(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeH264,
			Channels: 1,
		},
	}
	sb := createSampleBuilder(codec)
	require.NotNil(t, sb)
}

func TestOpusSampleBuilder(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus,
			Channels: 1,
		},
	}
	sb := createSampleBuilder(codec)
	require.NotNil(t, sb)
}

func TestVP9SampleBuilderUnsupportedCodec(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeVP9,
			Channels: 1,
		},
	}
	sb := createSampleBuilder(codec)
	require.Nil(t, sb)
}

func TestUnsupportedCodecSampleBuilder(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeAV1,
			Channels: 1,
		},
	}
	sb := createSampleBuilder(codec)
	require.Nil(t, sb)
}
