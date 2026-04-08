package lksdk

import (
	"crypto/aes"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

var (
	opusEncryptedFrame = []byte{120, 145, 24, 159, 76, 65, 130, 48, 144, 249, 17, 112, 134, 78, 250, 129, 171, 194, 16, 173, 73, 196, 5, 152, 69, 225, 28, 210, 196, 241, 226, 139, 231, 172, 51, 38, 139, 179, 245, 182, 170, 8, 122, 117, 98, 144, 123, 95, 73, 89, 119, 39, 205, 20, 191, 55, 121, 59, 239, 192, 85, 224, 228, 143, 10, 113, 195, 223, 118, 42, 2, 32, 22, 17, 77, 227, 109, 160, 245, 202, 189, 63, 162, 164, 5, 241, 24, 151, 45, 42, 165, 131, 171, 243, 141, 53, 35, 131, 141, 52, 253, 188, 12, 0}
	opusDecryptedFrame = []byte{120, 11, 109, 82, 113, 132, 189, 156, 220, 173, 30, 109, 87, 54, 173, 99, 26, 126, 166, 37, 127, 234, 110, 211, 230, 152, 181, 235, 197, 19, 140, 230, 179, 35, 131, 132, 29, 192, 97, 247, 108, 53, 183, 214, 77, 181, 173, 206, 175, 7, 228, 145, 93, 155, 155, 142, 14, 27, 111, 64, 96, 196, 229, 189, 142, 59, 149, 169, 99, 225, 216, 85, 186, 182}
	opusSilenceFrame   = []byte{
		0xf8, 0xff, 0xfe, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	sifTrailer         = []byte{50, 86, 10, 220, 108, 185, 57, 211}
	testPassphrase     = "12345"
	keyIncorrectLength = []byte{1, 2, 3}
)

func TestDeriveKeyFromString(t *testing.T) {

	password := "12345"

	key, err := DeriveKeyFromString(password)
	expectedKey := []byte{15, 94, 198, 66, 93, 211, 116, 46, 55, 97, 232, 121, 189, 233, 224, 22}

	require.Nil(t, err)
	require.Equal(t, key, expectedKey)
}

func TestDeriveKeyFromBytes(t *testing.T) {

	inputSecret := []byte{34, 21, 187, 202, 134, 204, 168, 62, 5, 105, 40, 244, 88}
	expectedKey := []byte{129, 224, 93, 62, 17, 203, 99, 136, 101, 35, 149, 128, 189, 152, 251, 76}

	key, err := DeriveKeyFromBytes(inputSecret)
	require.Nil(t, err)
	require.Equal(t, expectedKey, key)

}

func TestDecryptAudioSample(t *testing.T) {

	key, err := DeriveKeyFromString(testPassphrase)
	require.Nil(t, err)

	_, err = DecryptGCMAudioSample(opusEncryptedFrame, keyIncorrectLength, sifTrailer)
	require.ErrorIs(t, err, ErrIncorrectKeyLength)

	decryptedFrame, err := DecryptGCMAudioSample(nil, key, sifTrailer)
	require.Nil(t, err)
	require.Nil(t, decryptedFrame)

	decryptedFrame, err = DecryptGCMAudioSample(opusEncryptedFrame, key, sifTrailer)

	require.Nil(t, err)
	require.Equal(t, opusDecryptedFrame, decryptedFrame)

	var sifFrame []byte
	sifFrame = append(sifFrame, opusSilenceFrame...)
	sifFrame = append(sifFrame, sifTrailer...)

	decryptedFrame, err = DecryptGCMAudioSample(sifFrame, key, sifTrailer)
	require.Nil(t, err)
	require.Nil(t, decryptedFrame)

}

func TestEncryptAudioSample(t *testing.T) {

	key, err := DeriveKeyFromString(testPassphrase)
	require.Nil(t, err)

	_, err = EncryptGCMAudioSample(opusDecryptedFrame, keyIncorrectLength, 0)
	require.ErrorIs(t, err, ErrIncorrectKeyLength)

	encryptedFrame, err := EncryptGCMAudioSample(nil, key, 0)
	require.Nil(t, err)
	require.Nil(t, encryptedFrame)

	encryptedFrame, err = EncryptGCMAudioSample(opusDecryptedFrame, key, 0)

	require.Nil(t, err)

	// IV is generated randomly so to verify we decrypt and make sure that we got the expected plain text frame
	decryptedFrame, err := DecryptGCMAudioSample(encryptedFrame, key, sifTrailer)
	require.Nil(t, err)
	require.Equal(t, opusDecryptedFrame, decryptedFrame)

}

func BenchmarkDecryptAudioNewCipher(b *testing.B) {

	key, _ := DeriveKeyFromString(testPassphrase)
	for i := 0; i < b.N; i++ {
		cipherBlock, _ := aes.NewCipher(key)
		DecryptGCMAudioSampleCustomCipher(opusEncryptedFrame, sifTrailer, cipherBlock)

	}

}

func BenchmarkDecryptAudioCachedCipher(b *testing.B) {

	key, _ := DeriveKeyFromString(testPassphrase)
	cipherBlock, _ := aes.NewCipher(key)
	for i := 0; i < b.N; i++ {
		DecryptGCMAudioSampleCustomCipher(opusEncryptedFrame, sifTrailer, cipherBlock)

	}

}

func BenchmarkEncryptAudioCachedCipher(b *testing.B) {

	key, _ := DeriveKeyFromString(testPassphrase)
	cipherBlock, _ := aes.NewCipher(key)
	for i := 0; i < b.N; i++ {
		EncryptGCMAudioSampleCustomCipher(opusDecryptedFrame, 0, cipherBlock)

	}

}

func BenchmarkEncryptAudioNewCipher(b *testing.B) {

	key, _ := DeriveKeyFromString(testPassphrase)
	for i := 0; i < b.N; i++ {
		cipherBlock, _ := aes.NewCipher(key)
		EncryptGCMAudioSampleCustomCipher(opusDecryptedFrame, 0, cipherBlock)

	}

}

// ---------------------------------------------------------------------------
// H.264 video encryption tests
// ---------------------------------------------------------------------------

// makeH264Frame builds a minimal H.264 Annex-B frame with the given NALU types.
func makeH264Frame(naluTypes ...byte) []byte {
	var frame []byte
	for _, t := range naluTypes {
		frame = append(frame, 0x00, 0x00, 0x00, 0x01) // 4-byte start code
		frame = append(frame, t)                        // NALU header (1 byte)
		frame = append(frame, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22)
	}
	return frame
}

func TestEncryptH264Sample(t *testing.T) {
	key, err := DeriveKeyFromString(testPassphrase)
	require.NoError(t, err)

	// Frame with SPS (7) + PPS (8) + IDR slice (5).
	frame := makeH264Frame(7, 8, 5)

	encrypted, err := EncryptGCMH264Sample(frame, key, 0)
	require.NoError(t, err)
	require.NotEqual(t, frame, encrypted)

	// Round-trip decrypt.
	decrypted, err := DecryptGCMH264Sample(encrypted, key, nil)
	require.NoError(t, err)
	require.Equal(t, frame, decrypted)
}

func TestH264SpsOnlyPassthrough(t *testing.T) {
	key, err := DeriveKeyFromString(testPassphrase)
	require.NoError(t, err)

	// Frame with only SPS (7) + PPS (8) — no slice.
	frame := makeH264Frame(7, 8)

	encrypted, err := EncryptGCMH264Sample(frame, key, 0)
	require.NoError(t, err)
	// Should pass through unmodified.
	require.Equal(t, frame, encrypted)
}

func TestEncryptH264CustomCipher(t *testing.T) {
	key, err := DeriveKeyFromString(testPassphrase)
	require.NoError(t, err)
	block, err := aes.NewCipher(key)
	require.NoError(t, err)

	frame := makeH264Frame(7, 1) // SPS + non-IDR slice

	encrypted, err := EncryptGCMH264SampleCustomCipher(frame, 0, block)
	require.NoError(t, err)

	decrypted, err := DecryptGCMH264SampleCustomCipher(encrypted, nil, block)
	require.NoError(t, err)
	require.Equal(t, frame, decrypted)
}

// ---------------------------------------------------------------------------
// H.265 video encryption tests
// ---------------------------------------------------------------------------

// makeH265Frame builds a minimal H.265 Annex-B frame with the given NALU types.
// H.265 NALU header is 2 bytes: type in byte[0] bits [6:1], LayerID+TID in byte[1].
func makeH265Frame(naluTypes ...uint8) []byte {
	var frame []byte
	for _, t := range naluTypes {
		frame = append(frame, 0x00, 0x00, 0x00, 0x01) // 4-byte start code
		frame = append(frame, (t<<1)&0x7E, 0x01)       // NALU header (2 bytes): type in bits [6:1], TID=1
		frame = append(frame, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22)
	}
	return frame
}

func TestEncryptH265Sample(t *testing.T) {
	key, err := DeriveKeyFromString(testPassphrase)
	require.NoError(t, err)

	// Frame with VPS (32) + SPS (33) + PPS (34) + IDR_W_RADL (19).
	frame := makeH265Frame(32, 33, 34, 19)

	encrypted, err := EncryptGCMH265Sample(frame, key, 0)
	require.NoError(t, err)
	require.NotEqual(t, frame, encrypted)

	decrypted, err := DecryptGCMH265Sample(encrypted, key, nil)
	require.NoError(t, err)
	require.Equal(t, frame, decrypted)
}

func TestH265VpsOnlyPassthrough(t *testing.T) {
	key, err := DeriveKeyFromString(testPassphrase)
	require.NoError(t, err)

	// Frame with only VPS (32) + SPS (33) + PPS (34) — no slice.
	frame := makeH265Frame(32, 33, 34)

	encrypted, err := EncryptGCMH265Sample(frame, key, 0)
	require.NoError(t, err)
	require.Equal(t, frame, encrypted)
}

func TestEncryptH265CustomCipher(t *testing.T) {
	key, err := DeriveKeyFromString(testPassphrase)
	require.NoError(t, err)
	block, err := aes.NewCipher(key)
	require.NoError(t, err)

	frame := makeH265Frame(33, 0) // SPS + TRAIL_N slice

	encrypted, err := EncryptGCMH265SampleCustomCipher(frame, 0, block)
	require.NoError(t, err)

	decrypted, err := DecryptGCMH265SampleCustomCipher(encrypted, nil, block)
	require.NoError(t, err)
	require.Equal(t, frame, decrypted)
}

func TestH265AllSliceTypes(t *testing.T) {
	key, err := DeriveKeyFromString(testPassphrase)
	require.NoError(t, err)
	block, err := aes.NewCipher(key)
	require.NoError(t, err)

	// Test all valid VCL types from JS SDK isH265SliceNALU.
	sliceTypes := []uint8{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 16, 17, 18, 19, 20, 21}
	for _, st := range sliceTypes {
		frame := makeH265Frame(32, st) // VPS + slice
		encrypted, err := EncryptGCMH265SampleCustomCipher(frame, 0, block)
		require.NoError(t, err, "type %d", st)
		require.NotEqual(t, frame, encrypted, "type %d should encrypt", st)

		decrypted, err := DecryptGCMH265SampleCustomCipher(encrypted, nil, block)
		require.NoError(t, err, "type %d", st)
		require.Equal(t, frame, decrypted, "type %d round-trip", st)
	}

	// Test reserved types (10-15, 22-31) — should NOT be treated as slices.
	reservedTypes := []uint8{10, 11, 12, 13, 14, 15, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	for _, rt := range reservedTypes {
		frame := makeH265Frame(rt) // Only this NALU, no other slices
		encrypted, err := EncryptGCMH265SampleCustomCipher(frame, 0, block)
		require.NoError(t, err, "reserved type %d", rt)
		require.Equal(t, frame, encrypted, "reserved type %d should passthrough", rt)
	}
}

// ---------------------------------------------------------------------------
// Video encryption benchmarks
// ---------------------------------------------------------------------------

func BenchmarkEncryptH264CachedCipher(b *testing.B) {
	key, _ := DeriveKeyFromString(testPassphrase)
	block, _ := aes.NewCipher(key)
	frame := makeH264Frame(7, 8, 5)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncryptGCMH264SampleCustomCipher(frame, 0, block)
	}
}

func TestDataCryptorRoundTrip(t *testing.T) {
	kp := NewExternalKeyProvider()
	require.NoError(t, kp.SetKeyFromPassphrase(testPassphrase, 0))

	dc := NewDataCryptor(kp)

	// Create a DataPacket with user data.
	original := &livekit.DataPacket{
		Value: &livekit.DataPacket_User{
			User: &livekit.UserPacket{Payload: []byte("hello encrypted world")},
		},
	}

	encrypted, err := dc.Encrypt(original)
	require.NoError(t, err)

	// Should be wrapped in EncryptedPacket.
	ep, ok := encrypted.Value.(*livekit.DataPacket_EncryptedPacket)
	require.True(t, ok, "should be encrypted packet")
	require.NotEmpty(t, ep.EncryptedPacket.EncryptedValue)
	require.NotEmpty(t, ep.EncryptedPacket.Iv)

	// Decrypt.
	payload, err := dc.Decrypt(ep.EncryptedPacket)
	require.NoError(t, err)

	userPayload, ok := payload.Value.(*livekit.EncryptedPacketPayload_User)
	require.True(t, ok)
	require.Equal(t, []byte("hello encrypted world"), userPayload.User.Payload)
}

// ---------------------------------------------------------------------------
// Edge case / error path tests
// ---------------------------------------------------------------------------

func TestEncryptVideoNilCipher(t *testing.T) {
	_, err := EncryptGCMH264SampleCustomCipher(makeH264Frame(7, 5), 0, nil)
	require.ErrorIs(t, err, ErrBlockCipherRequired)

	_, err = EncryptGCMH265SampleCustomCipher(makeH265Frame(32, 19), 0, nil)
	require.ErrorIs(t, err, ErrBlockCipherRequired)
}

func TestDecryptVideoNilCipher(t *testing.T) {
	_, err := DecryptGCMH264SampleCustomCipher(makeH264Frame(7, 5), nil, nil)
	require.ErrorIs(t, err, ErrBlockCipherRequired)

	_, err = DecryptGCMH265SampleCustomCipher(makeH265Frame(32, 19), nil, nil)
	require.ErrorIs(t, err, ErrBlockCipherRequired)
}

func TestDecryptVideoTooShort(t *testing.T) {
	key, _ := DeriveKeyFromString(testPassphrase)
	block, _ := aes.NewCipher(key)

	// Samples shorter than 4 bytes pass through.
	short := []byte{0x01, 0x02}
	result, err := DecryptGCMH264SampleCustomCipher(short, nil, block)
	require.NoError(t, err)
	require.Equal(t, short, result)
}

func TestSetRawKeyValidation(t *testing.T) {
	kp := NewExternalKeyProvider()

	// Wrong length.
	err := kp.SetRawKey([]byte{1, 2, 3}, 0)
	require.ErrorIs(t, err, ErrIncorrectKeyLength)

	// Correct length (16 bytes).
	validKey := make([]byte, 16)
	err = kp.SetRawKey(validKey, 0)
	require.NoError(t, err)
}

func TestDeriveKeyFromStringEmpty(t *testing.T) {
	_, err := DeriveKeyFromString("")
	require.ErrorIs(t, err, ErrIncorrectSecretLength)
}

func BenchmarkEncryptH265CachedCipher(b *testing.B) {
	key, _ := DeriveKeyFromString(testPassphrase)
	block, _ := aes.NewCipher(key)
	frame := makeH265Frame(32, 33, 34, 19)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncryptGCMH265SampleCustomCipher(frame, 0, block)
	}
}
