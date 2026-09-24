// Copyright 2026 LiveKit, Inc.
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

package lksdk

import (
	"math"

	"github.com/livekit/server-sdk-go/v2/datatrack"
	"github.com/livekit/server-sdk-go/v2/e2ee"
	"github.com/livekit/server-sdk-go/v2/e2ee/types"
)

// dataTrackCryptor seals and opens data track frames with the room's DataCryptor, carrying the key
// index and IV in the packet header's E2EE extension.
type dataTrackCryptor struct {
	cryptor *e2ee.DataCryptor
}

func (c dataTrackCryptor) Encrypt(payload []byte) ([]byte, datatrack.E2EEExtension, error) {
	encrypted, err := c.cryptor.EncryptPayload(payload)
	if err != nil {
		return nil, datatrack.E2EEExtension{}, err
	}
	if encrypted.KeyIndex > math.MaxUint8 {
		return nil, datatrack.E2EEExtension{}, types.ErrKeyIndexOutOfRange
	}

	ext := datatrack.E2EEExtension{KeyIndex: uint8(encrypted.KeyIndex)}
	if len(encrypted.IV) != len(ext.IV) {
		return nil, datatrack.E2EEExtension{}, types.ErrIncorrectIVLength
	}
	copy(ext.IV[:], encrypted.IV)
	return encrypted.Ciphertext, ext, nil
}

func (c dataTrackCryptor) Decrypt(payload []byte, ext datatrack.E2EEExtension) ([]byte, error) {
	return c.cryptor.DecryptPayload(e2ee.EncryptedPayload{
		Ciphertext: payload,
		KeyIndex:   uint32(ext.KeyIndex),
		IV:         ext.IV[:],
	})
}

// dataTrackEncryptor returns the encryptor for the current session, or nil when data encryption
// is not enabled.
func (r *Room) dataTrackEncryptor() datatrack.Encryptor {
	if r.engine.dataCryptor == nil {
		return nil
	}
	return dataTrackCryptor{cryptor: r.engine.dataCryptor}
}

// dataTrackDecryptor returns the decryptor for the current session, or nil when data encryption
// is not enabled.
func (r *Room) dataTrackDecryptor() datatrack.Decryptor {
	if r.engine.dataCryptor == nil {
		return nil
	}
	return dataTrackCryptor{cryptor: r.engine.dataCryptor}
}
