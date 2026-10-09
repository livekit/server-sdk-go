// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2ee

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"sync"

	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/server-sdk-go/v2/e2ee/types"
)

// dataCipherState holds a cached cipher block along with the key material it
// was derived from, so the cache can detect when the keyProvider has refreshed
// a key at a given index and rebuild the block.
type dataCipherState struct {
	block    cipher.Block
	keyBytes []byte
}

// DataCryptor handles encryption and decryption of data channel messages and of raw payloads such
// as data track frames. It mirrors the JS SDK's DataCryptor class, using AES-128-GCM with no AAD.
type DataCryptor struct {
	keyProvider types.KeyProvider
	cipherCache map[uint32]*dataCipherState
	mu          sync.RWMutex
}

// NewDataCryptor creates a cryptor using the given key provider.
func NewDataCryptor(keyProvider types.KeyProvider) *DataCryptor {
	return &DataCryptor{
		keyProvider: keyProvider,
		cipherCache: make(map[uint32]*dataCipherState),
	}
}

// Encrypt wraps a DataPacket's value in an EncryptedPacket: the value is serialized as an
// EncryptedPacketPayload and sealed with EncryptPayload.
func (dc *DataCryptor) Encrypt(pck *livekit.DataPacket) (*livekit.DataPacket, error) {
	payload := DataPacketValueToPayload(pck)
	if payload == nil {
		return pck, nil // not an encryptable type, pass through
	}

	plaintext, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	encrypted, err := dc.EncryptPayload(plaintext)
	if err != nil {
		return nil, err
	}

	return &livekit.DataPacket{
		Kind:                  pck.Kind, //nolint:staticcheck
		ParticipantIdentity:   pck.ParticipantIdentity,
		DestinationIdentities: pck.DestinationIdentities,
		Value: &livekit.DataPacket_EncryptedPacket{
			EncryptedPacket: &livekit.EncryptedPacket{
				EncryptionType: livekit.Encryption_GCM,
				Iv:             encrypted.IV,
				KeyIndex:       encrypted.KeyIndex,
				EncryptedValue: encrypted.Ciphertext,
			},
		},
	}, nil
}

// Decrypt extracts and decrypts an EncryptedPacket, returning the inner payload.
func (dc *DataCryptor) Decrypt(ep *livekit.EncryptedPacket) (*livekit.EncryptedPacketPayload, error) {
	plaintext, err := dc.DecryptPayload(EncryptedPayload{Ciphertext: ep.EncryptedValue, KeyIndex: ep.KeyIndex, IV: ep.Iv})
	if err != nil {
		return nil, err
	}

	payload := &livekit.EncryptedPacketPayload{}
	if err := proto.Unmarshal(plaintext, payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	return payload, nil
}

// EncryptedPayload is a payload sealed by EncryptPayload together with what DecryptPayload needs
// to open it.
type EncryptedPayload struct {
	Ciphertext []byte
	KeyIndex   uint32
	IV         []byte
}

// EncryptPayload seals plaintext with the current key, using a random IV and no AAD.
func (dc *DataCryptor) EncryptPayload(plaintext []byte) (EncryptedPayload, error) {
	keyIndex := dc.keyProvider.CurrentKeyIndex()
	aesGCM, err := dc.gcm(keyIndex, types.IVLength)
	if err != nil {
		return EncryptedPayload{}, err
	}

	iv := make([]byte, types.IVLength)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return EncryptedPayload{}, fmt.Errorf("generate IV: %w", err)
	}

	return EncryptedPayload{
		Ciphertext: aesGCM.Seal(nil, iv, plaintext, nil),
		KeyIndex:   keyIndex,
		IV:         iv,
	}, nil
}

// DecryptPayload opens a payload sealed with the key at its key index.
func (dc *DataCryptor) DecryptPayload(payload EncryptedPayload) ([]byte, error) {
	if len(payload.IV) == 0 || len(payload.Ciphertext) == 0 {
		return nil, fmt.Errorf("empty IV or ciphertext")
	}

	aesGCM, err := dc.gcm(payload.KeyIndex, len(payload.IV))
	if err != nil {
		return nil, err
	}

	plaintext, err := aesGCM.Open(nil, payload.IV, payload.Ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

func (dc *DataCryptor) gcm(keyIndex uint32, nonceSize int) (cipher.AEAD, error) {
	block, err := dc.getCipherBlock(keyIndex)
	if err != nil {
		return nil, fmt.Errorf("get cipher for index %d: %w", keyIndex, err)
	}
	return cipher.NewGCMWithNonceSize(block, nonceSize)
}

// getCipherBlock returns an AES cipher block for the given key index. If the
// keyProvider has refreshed the key since we last cached it, the cached block
// is rebuilt from the current key material.
func (dc *DataCryptor) getCipherBlock(keyIndex uint32) (cipher.Block, error) {
	currentKey, err := dc.keyProvider.GetKey(keyIndex)
	if err != nil {
		return nil, err
	}

	dc.mu.RLock()
	if s, ok := dc.cipherCache[keyIndex]; ok && bytes.Equal(s.keyBytes, currentKey) {
		dc.mu.RUnlock()
		return s.block, nil
	}
	dc.mu.RUnlock()

	block, err := aes.NewCipher(currentKey)
	if err != nil {
		return nil, err
	}

	dc.mu.Lock()
	dc.cipherCache[keyIndex] = &dataCipherState{
		block:    block,
		keyBytes: append([]byte(nil), currentKey...),
	}
	dc.mu.Unlock()
	return block, nil
}

// DataPacketValueToPayload maps a DataPacket value to an EncryptedPacketPayload.
func DataPacketValueToPayload(pck *livekit.DataPacket) *livekit.EncryptedPacketPayload {
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

// DecryptedPayloadToDataPacketValue maps a decrypted EncryptedPacketPayload back to a DataPacket value.
func DecryptedPayloadToDataPacketValue(pck *livekit.DataPacket, payload *livekit.EncryptedPacketPayload) {
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
