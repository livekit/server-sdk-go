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

package datatrack

import (
	"bytes"
	"testing"
	"time"

	dtp "github.com/livekit/protocol/datatrack"
)

// testE2EEPrefix marks payloads "encrypted" by prefixingEncryptor.
var testE2EEPrefix = []byte{0xde, 0xad, 0xbe, 0xef}

// prefixingEncryptor marks payloads instead of encrypting them.
type prefixingEncryptor struct{}

func (prefixingEncryptor) Encrypt(payload []byte) ([]byte, dtp.ExtensionE2EE, error) {
	return append(bytes.Clone(testE2EEPrefix), payload...), dtp.ExtensionE2EE{}, nil
}

// prefixStrippingDecryptor undoes prefixingEncryptor.
type prefixStrippingDecryptor struct{}

func (prefixStrippingDecryptor) Decrypt(payload []byte, _ dtp.ExtensionE2EE) ([]byte, error) {
	return payload[len(testE2EEPrefix):], nil
}

// expectEvent receives from ch or fails fast. The 500 ms timeout is generous
// enough to avoid flakes on slow CI while keeping failures quick.
func expectEvent[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(500 * time.Millisecond):
		var zero T
		t.Fatalf("timed out waiting for %T", zero)
		return zero
	}
}

// expectNoEvent fails if ch delivers anything within the grace period.
func expectNoEvent[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case event := <-ch:
		t.Fatalf("unexpected event %v", event)
	case <-time.After(50 * time.Millisecond):
	}
}
