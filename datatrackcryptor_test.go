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
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/server-sdk-go/v2/e2ee"
	"github.com/livekit/server-sdk-go/v2/e2ee/types"
)

// fixedKeyProvider serves one key at any index, standing in for a provider without an index bound.
type fixedKeyProvider struct {
	index uint32
}

func (p fixedKeyProvider) GetKey(uint32) ([]byte, error) {
	return bytes.Repeat([]byte{0x11}, types.KeySizeBytes), nil
}

func (p fixedKeyProvider) CurrentKeyIndex() uint32 {
	return p.index
}

func TestDataTrackCryptorRoundTrip(t *testing.T) {
	kp := e2ee.NewExternalKeyProvider()
	require.NoError(t, kp.SetRawKey(bytes.Repeat([]byte{0x11}, types.KeySizeBytes), 3))
	c := dataTrackCryptor{cryptor: e2ee.NewDataCryptor(kp)}

	ciphertext, ext, err := c.Encrypt([]byte("hello encrypted world"))
	require.NoError(t, err)
	require.Equal(t, uint8(3), ext.KeyIndex)
	require.NotEqual(t, []byte("hello encrypted world"), ciphertext)

	plaintext, err := c.Decrypt(ciphertext, ext)
	require.NoError(t, err)
	require.Equal(t, []byte("hello encrypted world"), plaintext)
}

func TestDataTrackCryptorRejectsOutOfRangeKeyIndex(t *testing.T) {
	c := dataTrackCryptor{cryptor: e2ee.NewDataCryptor(fixedKeyProvider{index: 256})}

	_, _, err := c.Encrypt([]byte("hello"))
	require.ErrorIs(t, err, types.ErrKeyIndexOutOfRange)
}

func TestDataTrackCryptorWrongKeyFails(t *testing.T) {
	encryptor := dataTrackCryptor{cryptor: e2ee.NewDataCryptor(fixedKeyProvider{})}
	kp := e2ee.NewExternalKeyProvider()
	require.NoError(t, kp.SetRawKey(bytes.Repeat([]byte{0x22}, types.KeySizeBytes), 0))
	decryptor := dataTrackCryptor{cryptor: e2ee.NewDataCryptor(kp)}

	ciphertext, ext, err := encryptor.Encrypt([]byte("secret"))
	require.NoError(t, err)

	_, err = decryptor.Decrypt(ciphertext, ext)
	require.Error(t, err)
}
