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

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		path   string
		query  string
		want   string
	}{
		{"no trailing slash", "wss://host", "/rtc", "a=1&b=2", "wss://host/rtc?a=1&b=2"},
		{"trailing slash", "wss://host/", "/rtc", "a=1", "wss://host/rtc?a=1"},
		{"path without leading slash", "wss://host", "rtc", "a=1", "wss://host/rtc?a=1"},
		{"trailing slash and path without leading slash", "wss://host/", "rtc", "a=1", "wss://host/rtc?a=1"},
		{"path prefix with trailing slash", "wss://host/livekit/", "/rtc", "a=1", "wss://host/livekit/rtc?a=1"},
		{"query on prefix is replaced", "wss://host/?x=1", "/rtc", "a=1", "wss://host/rtc?a=1"},
		{"repeated trailing slashes", "wss://host//", "/rtc", "a=1", "wss://host/rtc?a=1"},
		{"validate path over https", "https://host/", "/rtc/validate", "a=1", "https://host/rtc/validate?a=1"},
		{"dot segments are resolved", "wss://host/a/../", "/rtc", "a=1", "wss://host/rtc?a=1"},
		{"percent-encoded dots are preserved", "wss://host/a/%2e%2e/", "/rtc", "a=1", "wss://host/a/%2e%2e/rtc?a=1"},
		{"port", "wss://host:7880/", "/rtc", "a=1", "wss://host:7880/rtc?a=1"},
		{"empty path", "wss://host/", "", "a=1", "wss://host/?a=1"},
		{"empty query", "wss://host/", "/rtc", "", "wss://host/rtc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := buildURL(tt.prefix, tt.path, tt.query)
			require.NoError(t, err)
			require.Equal(t, tt.want, u)
		})
	}
	t.Run("unparseable prefix", func(t *testing.T) {
		_, err := buildURL("://bad", "/rtc", "a=1")
		require.Error(t, err)
	})
}

func TestBuildURLMissingHost(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{"scheme-less host and port", "localhost:7880"},
		{"scheme-less host", "localhost"},
		{"opaque url", "wss:host"},
		{"path only", "/just/a/path"},
		{"empty host", "wss:///rtc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildURL(tt.prefix, "/rtc", "a=1")
			require.ErrorIs(t, err, ErrInvalidParameter)
		})
	}
	t.Run("password is redacted from the error", func(t *testing.T) {
		_, err := buildURL("wss://user:secret@", "/rtc", "a=1")
		require.ErrorIs(t, err, ErrInvalidParameter)
		require.NotContains(t, err.Error(), "secret")
	})
}
