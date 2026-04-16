package e2ee

import (
	"crypto/aes"
	"crypto/cipher"

	"github.com/livekit/server-sdk-go/v2/e2ee/types"
)

// EncryptGCMH265Sample encrypts an H.265 video sample with AES-128-GCM.
func EncryptGCMH265Sample(sample, key []byte, kid uint8) ([]byte, error) {
	if len(key) != types.KeySizeBytes {
		return nil, types.ErrIncorrectKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return EncryptGCMH265SampleCustomCipher(sample, kid, block)
}

// EncryptGCMH265SampleCustomCipher encrypts an H.265 video sample using a cached cipher.Block.
func EncryptGCMH265SampleCustomCipher(sample []byte, kid uint8, cipherBlock cipher.Block) ([]byte, error) {
	return encryptVideoSample(sample, kid, cipherBlock, findH265UnencryptedBytes)
}

// DecryptGCMH265Sample decrypts an H.265 video sample.
func DecryptGCMH265Sample(sample, key, sifTrailer []byte) ([]byte, error) {
	if len(key) != types.KeySizeBytes {
		return nil, types.ErrIncorrectKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return DecryptGCMH265SampleCustomCipher(sample, sifTrailer, block)
}

// DecryptGCMH265SampleCustomCipher decrypts an H.265 video sample using a cached cipher.Block.
func DecryptGCMH265SampleCustomCipher(sample, sifTrailer []byte, cipherBlock cipher.Block) ([]byte, error) {
	return decryptVideoSample(sample, sifTrailer, cipherBlock, findH265UnencryptedBytes)
}
