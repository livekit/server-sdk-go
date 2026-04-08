package lksdk

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

const (
	LIVEKIT_SDK_SALT         = "LKFrameEncryptionKey"
	LIVEKIT_IV_LENGTH        = 12
	LIVEKIT_PBKDF_ITERATIONS = 100000
	LIVEKIT_KEY_SIZE_BYTES   = 16
	LIVEKIT_HKDF_INFO_BYTES  = 128
	unencrypted_audio_bytes  = 1

	// H.264 NALU type constants.
	h264NaluTypeMask    = 0x1f
	h264NaluSliceNonIDR = 1
	h264NaluSliceIDR    = 5

	// H.265 NALU type mask (6-bit type in bits [6:1] of first header byte).
	h265NaluTypeMask = 0x3f
)

var (
	ErrIncorrectKeyLength    = errors.New("incorrect key length for encryption/decryption")
	ErrUnableGenerateIV      = errors.New("unable to generate iv for encryption")
	ErrIncorrectIVLength     = errors.New("incorrect iv length")
	ErrIncorrectSecretLength = errors.New("input secret provided to derivation function cannot be empty or nil")
	ErrIncorrectSaltLength   = errors.New("input salt provided to derivation function cannot be empty or nil")
	ErrBlockCipherRequired   = errors.New("input block cipher cannot be nil")
)

func DeriveKeyFromString(password string) ([]byte, error) {
	return DeriveKeyFromStringCustomSalt(password, LIVEKIT_SDK_SALT)
}

func DeriveKeyFromStringCustomSalt(password, salt string) ([]byte, error) {

	if password == "" {
		return nil, ErrIncorrectSecretLength
	}
	if salt == "" {
		return nil, ErrIncorrectSaltLength
	}

	encPassword := []byte(password)
	encSalt := []byte(salt)

	return pbkdf2.Key(encPassword, encSalt, LIVEKIT_PBKDF_ITERATIONS, LIVEKIT_KEY_SIZE_BYTES, sha256.New), nil

}

func DeriveKeyFromBytes(secret []byte) ([]byte, error) {
	return DeriveKeyFromBytesCustomSalt(secret, LIVEKIT_SDK_SALT)
}

func DeriveKeyFromBytesCustomSalt(secret []byte, salt string) ([]byte, error) {

	info := make([]byte, LIVEKIT_HKDF_INFO_BYTES)
	encSalt := []byte(salt)

	if secret == nil {
		return nil, ErrIncorrectSecretLength
	}
	if salt == "" {
		return nil, ErrIncorrectSaltLength
	}

	hkdfReader := hkdf.New(sha256.New, secret, encSalt, info)

	key := make([]byte, LIVEKIT_KEY_SIZE_BYTES)
	_, err := io.ReadFull(hkdfReader, key)
	if err != nil {
		return nil, err
	}

	return key, nil

}

// Take audio sample (body of RTP) encrypted by LiveKit client SDK, extract IV and decrypt using provided key
// If sample matches sifTrailer, it's considered to be a non-encrypted Server Injected Frame and nil is returned
// Use DecryptGCMAudioSampleCustomCipher with cached aes cipher block for better (30%) performance
func DecryptGCMAudioSample(sample, key, sifTrailer []byte) ([]byte, error) {

	if len(key) != 16 {
		return nil, ErrIncorrectKeyLength
	}

	if sifTrailer != nil && len(sample) >= len(sifTrailer) {
		possibleTrailer := sample[len(sample)-len(sifTrailer):]
		if bytes.Equal(possibleTrailer, sifTrailer) {
			// this is unencrypted Server Injected Frame (SIF) that should be dropped
			return nil, nil
		}

	}

	cipherBlock, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return DecryptGCMAudioSampleCustomCipher(sample, sifTrailer, cipherBlock)

}

// Take audio sample (body of RTP) encrypted by LiveKit client SDK, extract IV and decrypt using provided cipherBlock
// If sample matches sifTrailer, it's considered to be a non-encrypted Server Injected Frame and nil is returned
// Encrypted sample format based on livekit client sdk
// ---------+-------------------------+---------+----
// payload  |IV...(length = IV_LENGTH)|IV_LENGTH|KID|
// ---------+-------------------------+---------+----
// First byte of audio frame is not encrypted and only authenticated
// payload - variable bytes
// IV - variable bytes (equal to IV_LENGTH bytes)
// IV_LENGTH - 1 byte
// KID (Key ID) - 1 byte - ignored here, key is provided as parameter to function
func DecryptGCMAudioSampleCustomCipher(sample, sifTrailer []byte, cipherBlock cipher.Block) ([]byte, error) {

	if cipherBlock == nil {
		return nil, ErrBlockCipherRequired
	}

	if sample == nil {
		return nil, nil
	}

	if sifTrailer != nil && len(sample) >= len(sifTrailer) {
		possibleTrailer := sample[len(sample)-len(sifTrailer):]
		if bytes.Equal(possibleTrailer, sifTrailer) {
			// this is unencrypted Server Injected Frame (SIF) that should be dropped
			return nil, nil
		}

	}

	// variable naming is kept close to LiveKit client SDK decrypt function
	// https://github.com/livekit/client-sdk-js/blob/main/src/e2ee/worker/FrameCryptor.ts#L402

	frameHeader := sample[:unencrypted_audio_bytes] // first unencrypted bytes are "frameHeader" and used for authentication later
	frameTrailer := sample[len(sample)-2:]          // last 2 bytes having IV_LENGTH and KID (1 byte each)
	ivLength := int(frameTrailer[0])                // single byte, Endianness doesn't matter
	ivStart := len(sample) - len(frameTrailer) - ivLength
	if ivStart < 0 {
		return nil, ErrIncorrectIVLength
	}

	iv := make([]byte, ivLength)
	copy(iv, sample[ivStart:ivStart+ivLength]) // copy IV value out of sample into iv

	cipherTextStart := len(frameHeader)
	cipherTextLength := len(sample) - len(frameTrailer) - ivLength - len(frameHeader)
	cipherText := make([]byte, cipherTextLength)
	copy(cipherText, sample[cipherTextStart:cipherTextStart+cipherTextLength])

	aesGCM, err := cipher.NewGCMWithNonceSize(cipherBlock, ivLength) // standard Nonce size is 12 bytes, but since it MAY be different in the sample, we use the one from the sample
	if err != nil {
		return nil, err
	}

	plainText, err := aesGCM.Open(nil, iv, cipherText, frameHeader)
	if err != nil {
		return nil, err
	}

	newData := make([]byte, len(frameHeader)+len(plainText)) // allocate space for final packet

	_ = copy(newData[0:], frameHeader)              // put unencrypted frameHeader first
	_ = copy(newData[len(frameHeader):], plainText) // add decrypted remaining value

	return newData, nil

}

// Take audio sample (body of RTP) and encrypts it using AES-GCM 128bit with provided key
// Use EncryptGCMAudioSampleCustomCipher with cached aes cipher block for better (20%) performance
func EncryptGCMAudioSample(sample, key []byte, kid uint8) ([]byte, error) {

	if len(key) != 16 {
		return nil, ErrIncorrectKeyLength
	}

	cipherBlock, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return EncryptGCMAudioSampleCustomCipher(sample, kid, cipherBlock)

}

// Take audio sample (body of RTP) and encrypts it using AES-GCM 128bit with provided cipher block
// Encrypted sample format based on livekit client sdk
// ---------+-------------------------+---------+----
// payload  |IV...(length = IV_LENGTH)|IV_LENGTH|KID|
// ---------+-------------------------+---------+----
// First byte of audio frame is not encrypted and only authenticated
// payload - variable bytes
// IV - variable bytes (equal to IV_LENGTH bytes) - 12 random bytes
// IV_LENGTH - 1 byte - 12 bytes fixed
// KID (Key ID) - 1 byte - taken from "kid" parameter
func EncryptGCMAudioSampleCustomCipher(sample []byte, kid uint8, cipherBlock cipher.Block) ([]byte, error) {

	if cipherBlock == nil {
		return nil, ErrBlockCipherRequired
	}

	if sample == nil {
		return nil, nil
	}

	// variable naming is kept close to LiveKit client SDK decrypt function
	// https://github.com/livekit/client-sdk-js/blob/main/src/e2ee/worker/FrameCryptor.ts#L402

	frameHeader := append(make([]byte, 0), sample[:unencrypted_audio_bytes]...) // first unencrypted bytes are "frameHeader" and used for authentication later
	iv := make([]byte, LIVEKIT_IV_LENGTH)
	_, err := rand.Read(iv)
	if err != nil {
		return nil, errors.Join(ErrUnableGenerateIV, err)
	}

	frameTrailer := []byte{LIVEKIT_IV_LENGTH, kid} // last 2 bytes having IV_LENGTH and KID (1 byte each)

	plainTextStart := len(frameHeader)
	plainTextLength := len(sample) - len(frameHeader)
	plainText := make([]byte, plainTextLength)
	copy(plainText, sample[plainTextStart:plainTextStart+plainTextLength])

	aesGCM, err := cipher.NewGCMWithNonceSize(cipherBlock, LIVEKIT_IV_LENGTH) // standard Nonce size is 12 bytes, but using one from defined constant (which matches Javascript SDK)
	if err != nil {
		return nil, err
	}

	cipherText := aesGCM.Seal(nil, iv, plainText, frameHeader)

	newData := make([]byte, len(frameHeader)+len(cipherText)+len(iv)+len(frameTrailer)) // allocate space for final packet

	_ = copy(newData[0:], frameHeader)                                         // put unencrypted frameHeader first
	_ = copy(newData[len(frameHeader):], cipherText)                           // add cipherText
	_ = copy(newData[len(frameHeader)+len(cipherText):], iv)                   // add iv
	_ = copy(newData[len(frameHeader)+len(cipherText)+len(iv):], frameTrailer) // add trailer

	return newData, nil

}

// ---------------------------------------------------------------------------
// H.264 video sample encryption / decryption
// ---------------------------------------------------------------------------
// Compatible with the JS SDK FrameCryptor (FrameCryptor.ts + naluUtils.ts).
// Frame format (identical to audio but with H.264-aware header + RBSP escaping):
//
//	[frameHeader (unencrypted, AAD)][ciphertext+tag][IV][IV_LENGTH][KID]
//
// The encrypted portion is RBSP-escaped to prevent accidental H.264 start codes.
// Samples that contain no slice NALUs (e.g. SPS/PPS only) pass through unmodified.

// EncryptGCMH264Sample encrypts an H.264 video sample with AES-128-GCM.
// Use EncryptGCMH264SampleCustomCipher with a cached cipher.Block for better performance.
func EncryptGCMH264Sample(sample, key []byte, kid uint8) ([]byte, error) {
	if len(key) != LIVEKIT_KEY_SIZE_BYTES {
		return nil, ErrIncorrectKeyLength
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
	if len(key) != LIVEKIT_KEY_SIZE_BYTES {
		return nil, ErrIncorrectKeyLength
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

// ---------------------------------------------------------------------------
// H.265 video sample encryption / decryption
// ---------------------------------------------------------------------------
// Same frame format as H.264 but with H.265-aware NALU type parsing.
// Matches JS SDK naluUtils.ts parseH265NALUType / isH265SliceNALU.

// EncryptGCMH265Sample encrypts an H.265 video sample with AES-128-GCM.
func EncryptGCMH265Sample(sample, key []byte, kid uint8) ([]byte, error) {
	if len(key) != LIVEKIT_KEY_SIZE_BYTES {
		return nil, ErrIncorrectKeyLength
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
	if len(key) != LIVEKIT_KEY_SIZE_BYTES {
		return nil, ErrIncorrectKeyLength
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

// ---------------------------------------------------------------------------
// Shared video encryption/decryption core
// ---------------------------------------------------------------------------

// encryptVideoSample is the shared encryption core for H.264 and H.265.
// The findUnencrypted function determines how many header bytes to leave as AAD.
func encryptVideoSample(sample []byte, kid uint8, cipherBlock cipher.Block, findUnencrypted func([]byte) int) ([]byte, error) {
	if cipherBlock == nil {
		return nil, ErrBlockCipherRequired
	}
	if len(sample) < 4 {
		return sample, nil
	}

	unencryptedBytes := findUnencrypted(sample)
	if unencryptedBytes <= 0 || unencryptedBytes >= len(sample) {
		return sample, nil
	}

	frameHeader := make([]byte, unencryptedBytes)
	copy(frameHeader, sample[:unencryptedBytes])

	plainText := make([]byte, len(sample)-unencryptedBytes)
	copy(plainText, sample[unencryptedBytes:])

	iv := make([]byte, LIVEKIT_IV_LENGTH)
	if _, err := rand.Read(iv); err != nil {
		return nil, errors.Join(ErrUnableGenerateIV, err)
	}

	frameTrailer := []byte{LIVEKIT_IV_LENGTH, kid}

	aesGCM, err := cipher.NewGCMWithNonceSize(cipherBlock, LIVEKIT_IV_LENGTH)
	if err != nil {
		return nil, err
	}
	cipherText := aesGCM.Seal(nil, iv, plainText, frameHeader)

	encPortion := make([]byte, len(cipherText)+LIVEKIT_IV_LENGTH+2)
	copy(encPortion, cipherText)
	copy(encPortion[len(cipherText):], iv)
	copy(encPortion[len(cipherText)+LIVEKIT_IV_LENGTH:], frameTrailer)

	escaped := writeRBSP(encPortion)

	result := make([]byte, unencryptedBytes+len(escaped))
	copy(result, frameHeader)
	copy(result[unencryptedBytes:], escaped)
	return result, nil
}

// decryptVideoSample is the shared decryption core for H.264 and H.265.
func decryptVideoSample(sample, sifTrailer []byte, cipherBlock cipher.Block, findUnencrypted func([]byte) int) ([]byte, error) {
	if cipherBlock == nil {
		return nil, ErrBlockCipherRequired
	}
	if len(sample) < 4 {
		return sample, nil
	}

	if sifTrailer != nil && len(sample) >= len(sifTrailer) {
		if bytes.Equal(sample[len(sample)-len(sifTrailer):], sifTrailer) {
			return nil, nil
		}
	}

	unencryptedBytes := findUnencrypted(sample)
	if unencryptedBytes <= 0 || unencryptedBytes >= len(sample) {
		return sample, nil
	}

	frameHeader := sample[:unencryptedBytes]
	encryptedPortion := sample[unencryptedBytes:]

	if needsRBSPUnescaping(encryptedPortion) {
		encryptedPortion = parseRBSP(encryptedPortion)
	}

	if len(encryptedPortion) < 2+LIVEKIT_IV_LENGTH {
		return sample, nil
	}

	frameTrailer := encryptedPortion[len(encryptedPortion)-2:]
	ivLength := int(frameTrailer[0])
	if ivLength > len(encryptedPortion)-2 {
		return nil, ErrIncorrectIVLength
	}
	ivStart := len(encryptedPortion) - 2 - ivLength
	iv := make([]byte, ivLength)
	copy(iv, encryptedPortion[ivStart:ivStart+ivLength])

	cipherText := encryptedPortion[:ivStart]

	aesGCM, err := cipher.NewGCMWithNonceSize(cipherBlock, ivLength)
	if err != nil {
		return nil, err
	}
	plainText, err := aesGCM.Open(nil, iv, cipherText, frameHeader)
	if err != nil {
		return nil, err
	}

	result := make([]byte, len(frameHeader)+len(plainText))
	copy(result, frameHeader)
	copy(result[len(frameHeader):], plainText)
	return result, nil
}

// ---------------------------------------------------------------------------
// NALU helpers (matches JS SDK naluUtils.ts)
// ---------------------------------------------------------------------------

// findNALUIndices finds all NALU payload start positions (after the start code).
// Supports 3-byte (0x00 0x00 0x01) and 4-byte (0x00 0x00 0x00 0x01) start codes.
// Works for both H.264 and H.265 (same start code format).
func findNALUIndices(data []byte) []int {
	var indices []int
	i := 0
	for i < len(data)-3 {
		if i < len(data)-4 && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			indices = append(indices, i+4)
			i += 4
			continue
		}
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			indices = append(indices, i+3)
			i += 3
			continue
		}
		i++
	}
	return indices
}

// findH264UnencryptedBytes returns the byte offset up to and including the first
// 2 bytes of the first slice NALU (type 1=non-IDR or 5=IDR).
// Returns -1 if no slice NALU is found.
func findH264UnencryptedBytes(data []byte) int {
	for _, idx := range findNALUIndices(data) {
		if idx >= len(data) {
			continue
		}
		naluType := data[idx] & h264NaluTypeMask
		if naluType == h264NaluSliceNonIDR || naluType == h264NaluSliceIDR {
			return idx + 2
		}
	}
	return -1
}

// findH265UnencryptedBytes returns the byte offset up to and including the first
// 2 bytes of the first VCL slice NALU.
// H.265 NALU header is 2 bytes; type is in bits [6:1] of byte 0.
// Returns -1 if no slice NALU is found.
func findH265UnencryptedBytes(data []byte) int {
	for _, idx := range findNALUIndices(data) {
		if idx >= len(data) {
			continue
		}
		naluType := (data[idx] >> 1) & h265NaluTypeMask
		if isH265SliceNALU(naluType) {
			return idx + 2
		}
	}
	return -1
}

// isH265SliceNALU returns true if the NALU type is a VCL slice.
// Matches JS SDK isH265SliceNALU in naluUtils.ts exactly:
//   - Types 0-9:   TRAIL_N/R, TSA_N/R, STSA_N/R, RADL_N/R, RASL_N/R
//   - Types 16-21: BLA_W_LP, BLA_W_RADL, BLA_N_LP, IDR_W_RADL, IDR_N_LP, CRA_NUT
func isH265SliceNALU(naluType uint8) bool {
	return naluType <= 9 || (naluType >= 16 && naluType <= 21)
}

// ---------------------------------------------------------------------------
// RBSP byte-stuffing (matches JS SDK utils.ts writeRbsp / parseRbsp)
// ---------------------------------------------------------------------------

// writeRBSP applies RBSP byte-stuffing: inserts 0x03 emulation prevention bytes
// to avoid patterns 0x00 0x00 {0x00..0x03} in encrypted data.
func writeRBSP(data []byte) []byte {
	numConsecutiveZeros := 0
	var out []byte
	for _, b := range data {
		if b <= 3 && numConsecutiveZeros >= 2 {
			out = append(out, 3)
			numConsecutiveZeros = 0
		}
		out = append(out, b)
		if b == 0 {
			numConsecutiveZeros++
		} else {
			numConsecutiveZeros = 0
		}
	}
	return out
}

// parseRBSP removes RBSP emulation prevention bytes (0x03 after 0x00 0x00).
func parseRBSP(data []byte) []byte {
	var out []byte
	for i := 0; i < len(data); {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			out = append(out, 0, 0)
			i += 3
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
}

// needsRBSPUnescaping checks if data contains any RBSP emulation prevention bytes.
func needsRBSPUnescaping(data []byte) bool {
	for i := 0; i < len(data)-2; i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Start code normalization
// ---------------------------------------------------------------------------

// NormalizeStartCodes replaces all 3-byte H.264/H.265 start codes (00 00 01)
// with 4-byte start codes (00 00 00 01). This ensures the frame format matches
// what the browser produces after RTP depacketization (Chrome always uses 4-byte).
//
// Required on Linux where FFmpeg produces mixed 3-byte/4-byte start codes.
// Idempotent on Windows (already all 4-byte). Works for both H.264 and H.265.
func NormalizeStartCodes(data []byte) []byte {
	extra := 0
	i := 0
	for i < len(data)-2 {
		if i < len(data)-3 && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			i += 4
			continue
		}
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			extra++
			i += 3
			continue
		}
		i++
	}
	if extra == 0 {
		return data
	}

	out := make([]byte, 0, len(data)+extra)
	i = 0
	for i < len(data) {
		if i < len(data)-3 && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			out = append(out, 0, 0, 0, 1)
			i += 4
			continue
		}
		if i < len(data)-2 && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			out = append(out, 0, 0, 0, 1)
			i += 3
			continue
		}
		out = append(out, data[i])
		i++
	}
	return out
}
