package trackrecorder

import (
	"os"

	"github.com/pion/transport/packetio"
)

type Sink interface {
	Name() string
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
}

func NewFileSink(filename string) (Sink, error) {
	return os.Create(filename)
}

type bufferSink struct {
	id     string
	buffer *packetio.Buffer
}

func NewBufferSink(id string) Sink {
	buffer := packetio.NewBuffer()
	return &bufferSink{id, buffer}
}

func (s *bufferSink) Name() string {
	return s.id
}

func (s *bufferSink) Read(p []byte) (int, error) {
	return s.buffer.Read(p)
}

func (s *bufferSink) Write(p []byte) (int, error) {
	return s.buffer.Write(p)
}

func (s *bufferSink) Close() error {
	return s.buffer.Close()
}
