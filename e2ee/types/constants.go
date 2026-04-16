package types

import "errors"

const (
	SDKSalt         = "LKFrameEncryptionKey"
	IVLength        = 12
	PBKDFIterations = 100000
	KeySizeBytes    = 16
	HKDFInfoBytes   = 128
)

var (
	ErrIncorrectKeyLength  = errors.New("incorrect key length for encryption/decryption")
	ErrUnableGenerateIV    = errors.New("unable to generate iv for encryption")
	ErrIncorrectIVLength   = errors.New("incorrect iv length")
	ErrBlockCipherRequired = errors.New("input block cipher cannot be nil")
)
