package e2ee

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"golang.org/x/crypto/pbkdf2"

	"github.com/livekit/server-sdk-go/v2/e2ee/types"
)

// ExternalKeyProvider is a simple key provider where keys are set externally.
// This matches the JS SDK's ExternalE2EEKeyProvider pattern.
type ExternalKeyProvider struct {
	mu           sync.RWMutex
	keys         map[uint32][]byte
	currentIndex uint32
}

// NewExternalKeyProvider creates a new key provider for externally managed keys.
func NewExternalKeyProvider() *ExternalKeyProvider {
	return &ExternalKeyProvider{
		keys: make(map[uint32][]byte),
	}
}

// SetKeyFromPassphrase derives an AES-128 key from a passphrase using PBKDF2
// (matching the JS SDK's derivation: salt="LKFrameEncryptionKey", SHA-256,
// 100000 iterations, 128-bit output) and stores it at the given index.
func (p *ExternalKeyProvider) SetKeyFromPassphrase(passphrase string, index uint32) error {
	if passphrase == "" {
		return fmt.Errorf("passphrase cannot be empty")
	}
	derived := pbkdf2.Key(
		[]byte(passphrase),
		[]byte(types.SDKSalt),
		types.PBKDFIterations,
		types.KeySizeBytes,
		sha256.New,
	)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[index] = derived
	p.currentIndex = index
	return nil
}

// SetRawKey stores a raw AES-128 key (16 bytes) at the given index.
// Returns an error if the key length is not exactly 16 bytes.
func (p *ExternalKeyProvider) SetRawKey(key []byte, index uint32) error {
	if len(key) != types.KeySizeBytes {
		return types.ErrIncorrectKeyLength
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[index] = key
	p.currentIndex = index
	return nil
}

// GetKey returns the derived AES key for the given index.
func (p *ExternalKeyProvider) GetKey(keyIndex uint32) ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	key, ok := p.keys[keyIndex]
	if !ok {
		return nil, fmt.Errorf("no key at index %d", keyIndex)
	}
	return key, nil
}

// CurrentKeyIndex returns the active key index for encryption.
func (p *ExternalKeyProvider) CurrentKeyIndex() uint32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.currentIndex
}
