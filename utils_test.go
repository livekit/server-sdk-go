package lksdk

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
