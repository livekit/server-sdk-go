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

package signalling

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToHttpURL(t *testing.T) {
	t.Run("websocket input", func(t *testing.T) {
		require.Equal(t, "http://url.com", ToHttpURL("ws://url.com"))
	})
	t.Run("https input", func(t *testing.T) {
		require.Equal(t, "https://url.com", ToHttpURL("https://url.com"))
	})
}

func TestToWebsocketURL(t *testing.T) {
	t.Run("websocket input", func(t *testing.T) {
		require.Equal(t, "ws://url.com", ToWebsocketURL("ws://url.com"))
	})
	t.Run("https input", func(t *testing.T) {
		require.Equal(t, "wss://url.com", ToWebsocketURL("https://url.com"))
	})
}
