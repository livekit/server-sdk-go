package lksdk

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSignalClient_Join(t *testing.T) {
	t.Run("rejects empty URLs", func(t *testing.T) {
		c := NewSignalClient()
		_, err := c.Join("", "", &ConnectParams{})
		require.Equal(t, ErrURLNotProvided, err)
	})

	t.Run("errors on invalid URLs", func(t *testing.T) {
		c := NewSignalClient()
		_, err := c.Join("https://invalid-livekit-url", "", &ConnectParams{})
		require.Error(t, err)
		require.NotEqual(t, ErrURLNotProvided, err)
	})
}
