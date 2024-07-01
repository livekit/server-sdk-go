package lksdk

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAttributeChanges(t *testing.T) {
	diff := attributeChanges(map[string]string{
		"a": "1",
		"b": "2",
	}, map[string]string{
		"a": "2",
		"c": "3",
	})
	require.Equal(t, map[string]string{
		"a": "2",
		"b": "",
		"c": "3",
	}, diff)
}
