package lksdk

import (
	"sync"

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
	onWrite func(data T)
	onClose func()
}

func newBaseStreamWriter[T any](onWrite func(data T), onClose func()) *baseStreamWriter[T] {
	return &baseStreamWriter[T]{
		onWrite: onWrite,
		onClose: onClose,
	}
}

func (w *baseStreamWriter[T]) Write(data T) {
	w.onWrite(data)
}

func (w *baseStreamWriter[T]) Close() {
	w.onClose()
}

func writeStreamBytes(data []byte, e *RTCEngine, streamId string, destinationIdentities []string, totalSize *uint64, onProgress *func(progress float64)) {
	chunkIndex := uint64(0)
	for i := 0; i < len(data); i += STREAM_CHUNK_SIZE {
		end := i + STREAM_CHUNK_SIZE
		if end > len(data) {
			end = len(data)
		}

		e.waitForBufferStatusLow(protocol.DataPacket_RELIABLE)
		e.publishStreamChunk(&protocol.DataStream_Chunk{
			StreamId:   streamId,
			Content:    data[i:end],
			ChunkIndex: chunkIndex,
		}, destinationIdentities)

		if onProgress != nil && totalSize != nil {
			progress := float64(i) / float64(*totalSize)
			(*onProgress)(progress)
		}

		chunkIndex++
	}
}

type TextStreamWriter struct {
	*baseStreamWriter[string]
	Info TextStreamInfo
}

func newTextStreamWriter(info TextStreamInfo, header *protocol.DataStream_Header, e *RTCEngine, destinationIdentities []string, onProgress *func(progress float64)) *TextStreamWriter {
	e.publishStreamHeader(header, destinationIdentities)

	onWrite := func(data string) {
		writeStreamBytes([]byte(data), e, info.Id, destinationIdentities, info.Size, onProgress)
	}

	onClose := func() {
		e.publishStreamTrailer(info.Id, destinationIdentities)
	}

	return &TextStreamWriter{
		baseStreamWriter: newBaseStreamWriter[string](onWrite, onClose),
		Info:             info,
	}
}

type ByteStreamWriter struct {
	*baseStreamWriter[[]byte]
	Info ByteStreamInfo
}

func newByteStreamWriter(info ByteStreamInfo, header *protocol.DataStream_Header, e *RTCEngine, destinationIdentities []string, onProgress *func(progress float64)) *ByteStreamWriter {
	e.publishStreamHeader(header, destinationIdentities)

	onWrite := func(data []byte) {
		writeStreamBytes(data, e, info.Id, destinationIdentities, info.Size, onProgress)
	}

	onClose := func() {
		e.publishStreamTrailer(info.Id, destinationIdentities)
	}

	return &ByteStreamWriter{
		baseStreamWriter: newBaseStreamWriter[[]byte](onWrite, onClose),
		Info:             info,
	}
}

type baseStreamReader struct {
	reader        chan *protocol.DataStream_Chunk
	totalByteSize *uint64
	bytesReceived int

	closed     bool
	readerLock sync.Mutex

	onProgress *func(progress float64)
}

func newBaseStreamReader(totalByteSize *uint64) *baseStreamReader {
	baseReader := &baseStreamReader{
		reader:        make(chan *protocol.DataStream_Chunk),
		bytesReceived: 0,
	}
	if totalByteSize != nil {
		baseReader.totalByteSize = totalByteSize
	}
	return baseReader
}

func (r *baseStreamReader) enqueue(chunk *protocol.DataStream_Chunk) {
	r.readerLock.Lock()
	defer r.readerLock.Unlock()

	if r.closed {
		return
	}
	r.reader <- chunk
}

func (r *baseStreamReader) OnProgress(onProgress *func(progress float64)) {
	r.onProgress = onProgress
}

func (r *baseStreamReader) close() {
	r.readerLock.Lock()
	defer r.readerLock.Unlock()

	if !r.closed {
		close(r.reader)
		r.closed = true
	}
}

type TextStreamReader struct {
	*baseStreamReader
	Info TextStreamInfo

	// Q: what are we using this for?
	receivedChunks map[uint64]*protocol.DataStream_Chunk
}

func NewTextStreamReader(info TextStreamInfo, totalChunkCount *uint64) *TextStreamReader {
	return &TextStreamReader{
		baseStreamReader: newBaseStreamReader(totalChunkCount),
		Info:             info,
		receivedChunks:   make(map[uint64]*protocol.DataStream_Chunk),
	}
}

func (r *TextStreamReader) handleChunkReceived(chunk *protocol.DataStream_Chunk) {
	index := chunk.ChunkIndex
	previousChunkAtIndex, ok := r.receivedChunks[index]

	if ok && previousChunkAtIndex != nil && previousChunkAtIndex.Version > chunk.Version {
		// we have a newer version already,dropping the old one
		return
	}

	r.receivedChunks[index] = chunk
	r.bytesReceived += len(chunk.Content)

	if r.totalByteSize != nil && r.onProgress != nil {
		currentProgress := float64(r.bytesReceived) / float64(*r.totalByteSize)
		(*r.onProgress)(currentProgress)
	}
}

func (r *TextStreamReader) Read() <-chan string {
	readOnlyChan := make(chan string)

	go func() {
		defer close(readOnlyChan)

		for chunk := range r.reader {
			r.handleChunkReceived(chunk)
			readOnlyChan <- string(chunk.Content)
		}
	}()

	return readOnlyChan
}

func (r *TextStreamReader) ReadAll() string {
	// Q: what if part of the data is already read through Read()?

	result := ""
	reader := r.Read()
	for chunk := range reader {
		result += chunk
	}

	// Q: maybe worth calling onProgress here with 1.0?
	return result
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

func (r *ByteStreamReader) handleChunkReceived(chunk *protocol.DataStream_Chunk) {
	r.bytesReceived += len(chunk.Content)

	if r.totalByteSize != nil && r.onProgress != nil {
		currentProgress := float64(r.bytesReceived) / float64(*r.totalByteSize)
		(*r.onProgress)(currentProgress)
	}
}

func (r *ByteStreamReader) Read() <-chan []byte {
	readOnlyChan := make(chan []byte)

	go func() {
		defer close(readOnlyChan)

		for chunk := range r.reader {
			r.handleChunkReceived(chunk)
			readOnlyChan <- chunk.Content
		}
	}()

	return readOnlyChan
}

func (r *ByteStreamReader) ReadAll() []byte {
	// Q: JS is using a Set for this, go does not have a set, do we need one?
	result := []byte{}
	reader := r.Read()
	for chunk := range reader {
		result = append(result, chunk...)
	}
	return result
}

type TextStreamHandler func(reader *TextStreamReader, participantIdentity string)

type ByteStreamHandler func(reader *ByteStreamReader, participantIdentity string)
