package lksdk

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"sync"

	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

const (
	e2eeIVLength = 12
)

// EncryptionOptions configures end-to-end encryption for data channel messages.
// Pass to [WithDataEncryption] to enable: all outgoing data packets are
// encrypted and incoming EncryptedPacket messages are decrypted automatically.
//
// Video/audio track encryption is configured separately per-track via
// [TrackPublicationOptions] and EncryptGCM*Sample helpers.
type EncryptionOptions struct {
	KeyProvider KeyProvider
}

// KeyProvider manages encryption keys for E2EE.
type KeyProvider interface {
	// GetKey returns the derived AES key for the given index.
	GetKey(keyIndex uint32) ([]byte, error)
	// CurrentKeyIndex returns the active key index for encryption.
	CurrentKeyIndex() uint32
}

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
	derived, err := DeriveKeyFromString(passphrase)
	if err != nil {
		return fmt.Errorf("derive key: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[index] = derived
	p.currentIndex = index
	return nil
}

// SetRawKey stores a raw AES-128 key (16 bytes) at the given index.
// Returns an error if the key length is not exactly 16 bytes.
func (p *ExternalKeyProvider) SetRawKey(key []byte, index uint32) error {
	if len(key) != LIVEKIT_KEY_SIZE_BYTES {
		return ErrIncorrectKeyLength
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

// ---------------------------------------------------------------------------
// DataCryptor — data channel encryption / decryption
// ---------------------------------------------------------------------------

// DataCryptor handles encryption and decryption of data channel messages.
// It mirrors the JS SDK's DataCryptor class, using AES-128-GCM with no AAD.
type DataCryptor struct {
	keyProvider KeyProvider
	cipherCache map[uint32]cipher.Block
	mu          sync.RWMutex
}

// NewDataCryptor creates a cryptor using the given key provider.
func NewDataCryptor(keyProvider KeyProvider) *DataCryptor {
	return &DataCryptor{
		keyProvider: keyProvider,
		cipherCache: make(map[uint32]cipher.Block),
	}
}

// Encrypt wraps a DataPacket's value in an EncryptedPacket.
// The original value is serialized as EncryptedPacketPayload, then
// encrypted with AES-128-GCM using a random IV and no AAD.
func (dc *DataCryptor) Encrypt(pck *livekit.DataPacket) (*livekit.DataPacket, error) {
	payload := dataPacketValueToPayload(pck)
	if payload == nil {
		return pck, nil // not an encryptable type, pass through
	}

	plaintext, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	keyIndex := dc.keyProvider.CurrentKeyIndex()
	block, err := dc.getCipherBlock(keyIndex)
	if err != nil {
		return nil, fmt.Errorf("get cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCMWithNonceSize(block, e2eeIVLength)
	if err != nil {
		return nil, err
	}

	iv := make([]byte, e2eeIVLength)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, fmt.Errorf("generate IV: %w", err)
	}

	ciphertext := aesGCM.Seal(nil, iv, plaintext, nil)

	return &livekit.DataPacket{
		Kind:                  pck.Kind, //nolint:staticcheck
		ParticipantIdentity:   pck.ParticipantIdentity,
		DestinationIdentities: pck.DestinationIdentities,
		Value: &livekit.DataPacket_EncryptedPacket{
			EncryptedPacket: &livekit.EncryptedPacket{
				EncryptionType: livekit.Encryption_GCM,
				Iv:             iv,
				KeyIndex:       keyIndex,
				EncryptedValue: ciphertext,
			},
		},
	}, nil
}

// Decrypt extracts and decrypts an EncryptedPacket, returning the inner payload.
func (dc *DataCryptor) Decrypt(ep *livekit.EncryptedPacket) (*livekit.EncryptedPacketPayload, error) {
	if len(ep.Iv) == 0 || len(ep.EncryptedValue) == 0 {
		return nil, fmt.Errorf("empty IV or ciphertext")
	}

	block, err := dc.getCipherBlock(ep.KeyIndex)
	if err != nil {
		return nil, fmt.Errorf("get cipher for index %d: %w", ep.KeyIndex, err)
	}

	aesGCM, err := cipher.NewGCMWithNonceSize(block, len(ep.Iv))
	if err != nil {
		return nil, err
	}

	plaintext, err := aesGCM.Open(nil, ep.Iv, ep.EncryptedValue, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	payload := &livekit.EncryptedPacketPayload{}
	if err := proto.Unmarshal(plaintext, payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	return payload, nil
}

// getCipherBlock returns a cached or new AES cipher block for the given key index.
func (dc *DataCryptor) getCipherBlock(keyIndex uint32) (cipher.Block, error) {
	dc.mu.RLock()
	if block, ok := dc.cipherCache[keyIndex]; ok {
		dc.mu.RUnlock()
		return block, nil
	}
	dc.mu.RUnlock()

	key, err := dc.keyProvider.GetKey(keyIndex)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	dc.mu.Lock()
	dc.cipherCache[keyIndex] = block
	dc.mu.Unlock()
	return block, nil
}

// dataPacketValueToPayload maps a DataPacket value to an EncryptedPacketPayload.
func dataPacketValueToPayload(pck *livekit.DataPacket) *livekit.EncryptedPacketPayload {
	switch v := pck.Value.(type) {
	case *livekit.DataPacket_User:
		return &livekit.EncryptedPacketPayload{Value: &livekit.EncryptedPacketPayload_User{User: v.User}}
	case *livekit.DataPacket_ChatMessage:
		return &livekit.EncryptedPacketPayload{Value: &livekit.EncryptedPacketPayload_ChatMessage{ChatMessage: v.ChatMessage}}
	case *livekit.DataPacket_RpcRequest:
		return &livekit.EncryptedPacketPayload{Value: &livekit.EncryptedPacketPayload_RpcRequest{RpcRequest: v.RpcRequest}}
	case *livekit.DataPacket_RpcAck:
		return &livekit.EncryptedPacketPayload{Value: &livekit.EncryptedPacketPayload_RpcAck{RpcAck: v.RpcAck}}
	case *livekit.DataPacket_RpcResponse:
		return &livekit.EncryptedPacketPayload{Value: &livekit.EncryptedPacketPayload_RpcResponse{RpcResponse: v.RpcResponse}}
	case *livekit.DataPacket_StreamHeader:
		return &livekit.EncryptedPacketPayload{Value: &livekit.EncryptedPacketPayload_StreamHeader{StreamHeader: v.StreamHeader}}
	case *livekit.DataPacket_StreamChunk:
		return &livekit.EncryptedPacketPayload{Value: &livekit.EncryptedPacketPayload_StreamChunk{StreamChunk: v.StreamChunk}}
	case *livekit.DataPacket_StreamTrailer:
		return &livekit.EncryptedPacketPayload{Value: &livekit.EncryptedPacketPayload_StreamTrailer{StreamTrailer: v.StreamTrailer}}
	}
	return nil
}

// decryptedPayloadToDataPacketValue maps a decrypted EncryptedPacketPayload back to a DataPacket value.
func decryptedPayloadToDataPacketValue(pck *livekit.DataPacket, payload *livekit.EncryptedPacketPayload) {
	switch v := payload.Value.(type) {
	case *livekit.EncryptedPacketPayload_User:
		pck.Value = &livekit.DataPacket_User{User: v.User}
	case *livekit.EncryptedPacketPayload_ChatMessage:
		pck.Value = &livekit.DataPacket_ChatMessage{ChatMessage: v.ChatMessage}
	case *livekit.EncryptedPacketPayload_RpcRequest:
		pck.Value = &livekit.DataPacket_RpcRequest{RpcRequest: v.RpcRequest}
	case *livekit.EncryptedPacketPayload_RpcAck:
		pck.Value = &livekit.DataPacket_RpcAck{RpcAck: v.RpcAck}
	case *livekit.EncryptedPacketPayload_RpcResponse:
		pck.Value = &livekit.DataPacket_RpcResponse{RpcResponse: v.RpcResponse}
	case *livekit.EncryptedPacketPayload_StreamHeader:
		pck.Value = &livekit.DataPacket_StreamHeader{StreamHeader: v.StreamHeader}
	case *livekit.EncryptedPacketPayload_StreamChunk:
		pck.Value = &livekit.DataPacket_StreamChunk{StreamChunk: v.StreamChunk}
	case *livekit.EncryptedPacketPayload_StreamTrailer:
		pck.Value = &livekit.DataPacket_StreamTrailer{StreamTrailer: v.StreamTrailer}
	}
}
