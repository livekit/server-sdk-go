package lksdk

import (
	"bytes"
	"sync"
	"time"

	protocol "github.com/livekit/protocol/livekit"
)

type baseStreamInfo struct {
	Id         string
	MimeType   string
	Topic      string
	Timestamp  int64
	Size       *uint64
	Attributes map[string]string
}

type ByteStreamInfo struct {
	*baseStreamInfo
	Name *string
}

type TextStreamInfo struct {
	*baseStreamInfo
}

const (
	STREAM_CHUNK_SIZE = 15_000
)

type StreamTextOptions struct {
	Topic                 string
	DestinationIdentities []string
	StreamId              *string
	ReplyToStreamId       *string
	TotalSize             *uint64
	Attributes            map[string]string
	OnProgress            *func(progress float64)
}

type StreamBytesOptions struct {
	Topic                 string
	MimeType              string
	DestinationIdentities []string
	StreamId              *string
	TotalSize             *uint64
	Attributes            map[string]string
	OnProgress            *func(progress float64)
	FileName              *string
}

type baseStreamWriter[T any] struct {
	onWrite func(data T) (int, error)
	onClose func() error
}

func newBaseStreamWriter[T any](onWrite func(data T) (int, error), onClose func() error) *baseStreamWriter[T] {
	return &baseStreamWriter[T]{
		onWrite: onWrite,
		onClose: onClose,
	}
}

// is it okay for TextStreamWriter to also return number of bytes written?
func (w *baseStreamWriter[T]) Write(data T) (int, error) {
	return w.onWrite(data)
}

func (w *baseStreamWriter[T]) Close() error {
	return w.onClose()
}

func writeStreamBytes(data []byte, e *RTCEngine, streamId string, destinationIdentities []string, totalSize *uint64, onProgress *func(progress float64)) (int, error) {
	chunkIndex := uint64(0)
	bytesWritten := 0
	for i := 0; i < len(data); i += STREAM_CHUNK_SIZE {
		end := i + STREAM_CHUNK_SIZE
		if end > len(data) {
			end = len(data)
		}

		// this is a blocking call, but, if we call it in a goroutine internally,
		// error propagation will require some blocking code which ruins the point
		// is it best for the user to call this in a goroutine?
		// or do we not block on buffer status low and just let the user handle it?
		err := e.waitForBufferStatusLow(protocol.DataPacket_RELIABLE)
		if err != nil {
			return bytesWritten, err
		}

		if err := e.publishStreamChunk(&protocol.DataStream_Chunk{
			StreamId:   streamId,
			Content:    data[i:end],
			ChunkIndex: chunkIndex,
		}, destinationIdentities); err != nil {
			return bytesWritten, err
		}

		if onProgress != nil && totalSize != nil {
			progress := float64(i) / float64(*totalSize)
			(*onProgress)(progress)
		}

		chunkIndex++
		bytesWritten += end - i
	}

	return bytesWritten, nil
}

type TextStreamWriter struct {
	*baseStreamWriter[string]
	Info TextStreamInfo
}

func newTextStreamWriter(info TextStreamInfo, header *protocol.DataStream_Header, e *RTCEngine, destinationIdentities []string, onProgress *func(progress float64)) (*TextStreamWriter, error) {
	err := e.publishStreamHeader(header, destinationIdentities)
	if err != nil {
		return nil, err
	}

	onWrite := func(data string) (int, error) {
		return writeStreamBytes([]byte(data), e, info.Id, destinationIdentities, info.Size, onProgress)
	}

	onClose := func() error {
		// what happens if user disconnects before sending the trailer?
		return e.publishStreamTrailer(info.Id, destinationIdentities)
	}

	return &TextStreamWriter{
		baseStreamWriter: newBaseStreamWriter[string](onWrite, onClose),
		Info:             info,
	}, nil
}

type ByteStreamWriter struct {
	*baseStreamWriter[[]byte]
	Info ByteStreamInfo
}

func newByteStreamWriter(info ByteStreamInfo, header *protocol.DataStream_Header, e *RTCEngine, destinationIdentities []string, onProgress *func(progress float64)) (*ByteStreamWriter, error) {
	err := e.publishStreamHeader(header, destinationIdentities)
	if err != nil {
		return nil, err
	}

	onWrite := func(data []byte) (int, error) {
		return writeStreamBytes(data, e, info.Id, destinationIdentities, info.Size, onProgress)
	}

	onClose := func() error {
		return e.publishStreamTrailer(info.Id, destinationIdentities)
	}

	return &ByteStreamWriter{
		baseStreamWriter: newBaseStreamWriter[[]byte](onWrite, onClose),
		Info:             info,
	}, nil
}

type baseStreamReader struct {
	readBuffer    bytes.Buffer
	totalByteSize *uint64
	bytesReceived int

	closed     bool
	closedLock sync.Mutex

	onProgress *func(progress float64)
}

func newBaseStreamReader(totalByteSize *uint64) *baseStreamReader {
	baseReader := &baseStreamReader{
		bytesReceived: 0,
	}
	if totalByteSize != nil {
		baseReader.totalByteSize = totalByteSize
	}
	return baseReader
}

func (r *baseStreamReader) enqueue(chunk *protocol.DataStream_Chunk) {
	r.closedLock.Lock()
	defer r.closedLock.Unlock()

	if r.closed {
		return
	}
	// write seems to handle growing the buffer if needed
	r.readBuffer.Write(chunk.Content)
}

func (r *baseStreamReader) OnProgress(onProgress *func(progress float64)) {
	r.onProgress = onProgress
}

func (r *baseStreamReader) maybeCallOnProgress(n int) {
	r.bytesReceived += n

	if r.totalByteSize != nil && r.onProgress != nil {
		currentProgress := float64(r.bytesReceived) / float64(*r.totalByteSize)
		(*r.onProgress)(currentProgress)
	}
}

func (r *baseStreamReader) Read(bytes []byte) (int, error) {
	n, err := r.readBuffer.Read(bytes)
	r.maybeCallOnProgress(n)
	return n, err
}

func (r *baseStreamReader) ReadByte() (byte, error) {
	n, err := r.readBuffer.ReadByte()
	r.maybeCallOnProgress(1)
	return n, err
}

func (r *baseStreamReader) ReadBytes(delim byte) ([]byte, error) {
	n, err := r.readBuffer.ReadBytes(delim)
	r.maybeCallOnProgress(len(n))
	return n, err
}

func (r *baseStreamReader) close() {
	r.closedLock.Lock()
	defer r.closedLock.Unlock()

	if !r.closed {
		r.closed = true
	}
}

type TextStreamReader struct {
	*baseStreamReader
	Info TextStreamInfo
}

func NewTextStreamReader(info TextStreamInfo, totalChunkCount *uint64) *TextStreamReader {
	return &TextStreamReader{
		baseStreamReader: newBaseStreamReader(totalChunkCount),
		Info:             info,
	}
}

func (r *TextStreamReader) ReadRune() (rune, int, error) {
	n, size, err := r.readBuffer.ReadRune()
	r.maybeCallOnProgress(size)
	return n, size, err
}

func (r *TextStreamReader) ReadString(delim byte) (string, error) {
	n, err := r.readBuffer.ReadString(delim)
	r.maybeCallOnProgress(len(n))
	return n, err
}

func (r *TextStreamReader) ReadAll() string {
	// wait for the stream to be closed
	// not sure if this is the best way to do this
	for {
		r.closedLock.Lock()
		if r.closed {
			r.closedLock.Unlock()
			break
		}
		r.closedLock.Unlock()
		time.Sleep(10 * time.Millisecond)
	}

	// Now that the stream is closed, read all data
	n := r.readBuffer.String()
	return n
}

type ByteStreamReader struct {
	*baseStreamReader
	Info ByteStreamInfo
}

func NewByteStreamReader(info ByteStreamInfo, totalChunkCount *uint64) *ByteStreamReader {
	return &ByteStreamReader{
		baseStreamReader: newBaseStreamReader(totalChunkCount),
		Info:             info,
	}
}

func (r *ByteStreamReader) ReadAll() []byte {
	// wait for the stream to be closed
	// not sure if this is the best way to do this
	for {
		r.closedLock.Lock()
		if r.closed {
			r.closedLock.Unlock()
			break
		}
		r.closedLock.Unlock()
		time.Sleep(10 * time.Millisecond)
	}

	// Now that the stream is closed, read all data
	n := r.readBuffer.Bytes()
	r.maybeCallOnProgress(len(n))
	return n
}

type TextStreamHandler func(reader *TextStreamReader, participantIdentity string)

type ByteStreamHandler func(reader *ByteStreamReader, participantIdentity string)
