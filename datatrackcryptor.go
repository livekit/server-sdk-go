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

	dtp "github.com/livekit/protocol/datatrack"

	"github.com/livekit/server-sdk-go/v2/e2ee"
	"github.com/livekit/server-sdk-go/v2/e2ee/types"
	"github.com/livekit/server-sdk-go/v2/internal/datatrack"
)

// dataTrackCryptor seals and opens data track frames with the room's DataCryptor, carrying the key
// index and IV in the packet header's E2EE extension.
type dataTrackCryptor struct {
	cryptor *e2ee.DataCryptor
}

func (c dataTrackCryptor) Encrypt(payload []byte) ([]byte, dtp.ExtensionE2EE, error) {
	encrypted, err := c.cryptor.EncryptPayload(payload)
	if err != nil {
		return nil, dtp.ExtensionE2EE{}, err
	}
	if encrypted.KeyIndex > math.MaxUint8 {
		return nil, dtp.ExtensionE2EE{}, types.ErrKeyIndexOutOfRange
	}

	var iv [dtp.ExtensionE2EEIVLength]byte
	if len(encrypted.IV) != len(iv) {
		return nil, dtp.ExtensionE2EE{}, types.ErrIncorrectIVLength
	}
	copy(iv[:], encrypted.IV)
	return encrypted.Ciphertext, *dtp.NewExtensionE2EE(uint8(encrypted.KeyIndex), iv), nil
}

func (c dataTrackCryptor) Decrypt(payload []byte, ext dtp.ExtensionE2EE) ([]byte, error) {
	iv := ext.IV()
	return c.cryptor.DecryptPayload(e2ee.EncryptedPayload{
		Ciphertext: payload,
		KeyIndex:   uint32(ext.KeyIndex()),
		IV:         iv[:],
	})
}

// dataTrackEncryptor returns the encryptor for the current session, or nil when data encryption
// is not enabled.
func (e *RTCEngine) dataTrackEncryptor() datatrack.Encryptor {
	if e.dataCryptor == nil {
		return nil
	}
	return dataTrackCryptor{cryptor: e.dataCryptor}
}

// dataTrackDecryptor returns the decryptor for the current session, or nil when data encryption
// is not enabled.
func (e *RTCEngine) dataTrackDecryptor() datatrack.Decryptor {
	if e.dataCryptor == nil {
		return nil
	}
	return dataTrackCryptor{cryptor: e.dataCryptor}
}
