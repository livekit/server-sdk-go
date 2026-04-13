package types

// KeyProvider manages encryption keys for E2EE.
type KeyProvider interface {
	// GetKey returns the derived AES key for the given index.
	GetKey(keyIndex uint32) ([]byte, error)
	// CurrentKeyIndex returns the active key index for encryption.
	CurrentKeyIndex() uint32
}

// FrameEncryptor encrypts a complete media frame (audio or video access unit)
// before RTP packetization. Implementations are codec-aware: the codec is
// configured at construction time, not at each call.
type FrameEncryptor interface {
	EncryptFrame(payload []byte) ([]byte, error)
}

// FrameDecryptor decrypts a reassembled media frame after RTP depacketization.
// Returns (nil, nil) for server-injected frames (SIF) that should be dropped.
type FrameDecryptor interface {
	DecryptFrame(payload []byte) ([]byte, error)
}
