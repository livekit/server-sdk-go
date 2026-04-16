package e2ee

import (
	"crypto/aes"
	"crypto/cipher"

	"github.com/livekit/server-sdk-go/v2/e2ee/types"
)

// EncryptGCMH264Sample encrypts an H.264 video sample with AES-128-GCM.
// Use EncryptGCMH264SampleCustomCipher with a cached cipher.Block for better performance.
func EncryptGCMH264Sample(sample, key []byte, kid uint8) ([]byte, error) {
	if len(key) != types.KeySizeBytes {
		return nil, types.ErrIncorrectKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return EncryptGCMH264SampleCustomCipher(sample, kid, block)
}

// EncryptGCMH264SampleCustomCipher encrypts an H.264 video sample using a cached cipher.Block.
func EncryptGCMH264SampleCustomCipher(sample []byte, kid uint8, cipherBlock cipher.Block) ([]byte, error) {
	return encryptVideoSample(sample, kid, cipherBlock, findH264UnencryptedBytes)
}

// DecryptGCMH264Sample decrypts an H.264 video sample encrypted by
// EncryptGCMH264Sample or the JS SDK FrameCryptor.
func DecryptGCMH264Sample(sample, key, sifTrailer []byte) ([]byte, error) {
	if len(key) != types.KeySizeBytes {
		return nil, types.ErrIncorrectKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return DecryptGCMH264SampleCustomCipher(sample, sifTrailer, block)
}

// DecryptGCMH264SampleCustomCipher decrypts an H.264 video sample using a cached cipher.Block.
func DecryptGCMH264SampleCustomCipher(sample, sifTrailer []byte, cipherBlock cipher.Block) ([]byte, error) {
	return decryptVideoSample(sample, sifTrailer, cipherBlock, findH264UnencryptedBytes)
}
