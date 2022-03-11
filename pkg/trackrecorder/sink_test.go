package trackrecorder

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateFileSink(t *testing.T) {
	filename := "testing.txt"
	sink, err := NewFileSink(filename)
	require.NoError(t, err)
	require.NotNil(t, sink)
	require.Equal(t, filename, sink.Name())

	// Cleanup
	err = sink.Close()
	require.NoError(t, err)
	os.Remove(filename)
}

func TestWriteFileSink(t *testing.T) {
	filename := "testing.txt"
	sink, _ := NewFileSink(filename)
	defer func() {
		sink.Close()
		os.Remove(filename)
	}()

	n, err := sink.Write([]byte("Hello"))
	require.NoError(t, err)
	require.Equal(t, len("Hello"), n)
}

func TestCreateBufferSink(t *testing.T) {
	id := "testing"
	sink := NewBufferSink(id)
	require.NotNil(t, sink)
	require.Equal(t, id, sink.Name())

	err := sink.Close()
	require.NoError(t, err)
}

func TestWriteBufferSink(t *testing.T) {
	id := "testing"
	sink := NewBufferSink(id)
	defer sink.Close()

	p := []byte{'c', 'g', 'c'}
	n, err := sink.Write(p)
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

func TestReadBufferSink(t *testing.T) {
	id := "testing"
	sink := NewBufferSink(id)
	defer sink.Close()

	p := []byte{'c', 'g', 'c'}
	r := make([]byte, 3)
	sink.Write(p)
	n, err := sink.Read(r)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Equal(t, "cgc", string(r))
}
