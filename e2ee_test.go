package lksdk

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/server-sdk-go/v2/e2ee"
)

// TestNewFrameEncryptorDispatch confirms each Codec routes to the matching
// EncryptFunc and that its output round-trips through a NewFrameDecryptor
// of the same codec byte-for-byte.
func TestNewFrameEncryptorDispatch(t *testing.T) {
	cases := []struct {
		name  string
		codec Codec
		frame []byte
	}{
		{"H264", CodecH264, makeH264Frame(7, 8, 5)},       // SPS + PPS + IDR slice
		{"H265", CodecH265, makeH265Frame(32, 33, 19)},    // VPS + SPS + IDR slice
		{"Opus", CodecOpus, append([]byte{0xfc}, make([]byte, 40)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kp := e2ee.NewExternalKeyProvider()
			require.NoError(t, kp.SetKeyFromPassphrase(testPassphrase, 0))

			enc, err := NewFrameEncryptor(kp, tc.codec)
			require.NoError(t, err)
			require.NotNil(t, enc)

			dec, err := NewFrameDecryptor(kp, tc.codec, nil)
			require.NoError(t, err)
			require.NotNil(t, dec)

			ciphertext, err := enc.EncryptFrame(tc.frame)
			require.NoError(t, err)
			require.NotEqual(t, tc.frame, ciphertext, "ciphertext must differ from plaintext")

			plaintext, err := dec.DecryptFrame(ciphertext)
			require.NoError(t, err)
			require.Equal(t, tc.frame, plaintext, "round-trip must be byte-exact")
		})
	}
}

func TestNewFrameEncryptorUnsupportedCodec(t *testing.T) {
	kp := e2ee.NewExternalKeyProvider()
	require.NoError(t, kp.SetKeyFromPassphrase(testPassphrase, 0))

	_, err := NewFrameEncryptor(kp, Codec(999))
	require.Error(t, err)

	_, err = NewFrameDecryptor(kp, Codec(999), nil)
	require.Error(t, err)
}
