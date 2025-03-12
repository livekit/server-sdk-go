package lksdk

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
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

var EAGAIN = errors.New("there is no data available right now, try again later")

type StreamTextOptions struct {
	Topic                 string
	DestinationIdentities []string
	StreamId              *string
	ReplyToStreamId       *string
	TotalSize             uint64
	Attributes            map[string]string
	OnProgress            *func(progress float64)
}

type StreamBytesOptions struct {
	Topic                 string
	MimeType              string
	DestinationIdentities []string
	StreamId              *string
	TotalSize             uint64
	Attributes            map[string]string
	OnProgress            *func(progress float64)
	FileName              *string
}

type writeTask struct {
	data   []byte
	onDone *func()
}

type baseStreamWriter[T any] struct {
	engine                *RTCEngine
	streamId              string
	destinationIdentities []string
	totalSize             *uint64
	onProgress            *func(progress float64)

	chunkIndex uint64
	closed     atomic.Bool
	lock       sync.Mutex

	writeQueue chan writeTask
}

func newBaseStreamWriter[T any](engine *RTCEngine, streamId string, destinationIdentities []string, totalSize *uint64, onProgress *func(progress float64)) *baseStreamWriter[T] {
	base := &baseStreamWriter[T]{
		engine:                engine,
		streamId:              streamId,
		destinationIdentities: destinationIdentities,
		totalSize:             totalSize,
		onProgress:            onProgress,
		writeQueue:            make(chan writeTask),
	}

	go base.processWriteQueue()
	return base
}

func (w *baseStreamWriter[T]) processWriteQueue() {
	for task := range w.writeQueue {
		w.writeStreamBytes(task.data, task.onDone)
	}
}

func (w *baseStreamWriter[T]) Write(data T, onDone *func()) {
	if w.closed.Load() {
		return
	}

	switch v := any(data).(type) {
	case []byte:
		w.writeQueue <- writeTask{
			data:   v,
			onDone: onDone,
		}
	case string:
		w.writeQueue <- writeTask{
			data:   []byte(v),
			onDone: onDone,
		}
	}
}

func (w *baseStreamWriter[T]) Close() {
	if !w.closed.Load() {
		w.closed.Store(true)

		w.lock.Lock()
		err := w.engine.publishStreamTrailer(w.streamId, w.destinationIdentities)
		if err != nil {
			w.engine.log.Errorw("could not publish stream trailer", err)
		}
		w.lock.Unlock()
	}
}

func (w *baseStreamWriter[T]) writeStreamBytes(data []byte, onDone *func()) {
	w.lock.Lock()
	chunkIndex := w.chunkIndex

	for i := 0; i < len(data) && !w.closed.Load(); i += STREAM_CHUNK_SIZE {
		end := i + STREAM_CHUNK_SIZE
		if end > len(data) {
			end = len(data)
		}

		w.engine.waitForBufferStatusLow(protocol.DataPacket_RELIABLE)

		if err := w.engine.publishStreamChunk(&protocol.DataStream_Chunk{
			StreamId:   w.streamId,
			Content:    data[i:end],
			ChunkIndex: chunkIndex,
		}, w.destinationIdentities); err != nil {
			w.engine.log.Errorw("could not publish stream chunk", err)
		}

		if w.onProgress != nil && w.totalSize != nil {
			progress := float64(i) / float64(*w.totalSize)
			(*w.onProgress)(progress)
		}

		chunkIndex++
	}

	w.chunkIndex = chunkIndex
	w.lock.Unlock()

	if onDone != nil {
		(*onDone)()
	}
}

type TextStreamWriter struct {
	*baseStreamWriter[string]
	Info TextStreamInfo
}

func newTextStreamWriter(info TextStreamInfo, header *protocol.DataStream_Header, e *RTCEngine, destinationIdentities []string, onProgress *func(progress float64)) *TextStreamWriter {
	err := e.publishStreamHeader(header, destinationIdentities)
	if err != nil {
		e.log.Errorw("could not publish stream header", err)
	}

	return &TextStreamWriter{
		baseStreamWriter: newBaseStreamWriter[string](e, info.Id, destinationIdentities, info.Size, onProgress),
		Info:             info,
	}
}

type ByteStreamWriter struct {
	*baseStreamWriter[[]byte]
	Info ByteStreamInfo
}

func newByteStreamWriter(info ByteStreamInfo, header *protocol.DataStream_Header, e *RTCEngine, destinationIdentities []string, onProgress *func(progress float64)) *ByteStreamWriter {
	err := e.publishStreamHeader(header, destinationIdentities)
	if err != nil {
		e.log.Errorw("could not publish stream header", err)
	}

	return &ByteStreamWriter{
		baseStreamWriter: newBaseStreamWriter[[]byte](e, info.Id, destinationIdentities, info.Size, onProgress),
		Info:             info,
	}
}

type baseStreamReader struct {
	readBuffer    bytes.Buffer
	totalByteSize *uint64
	bytesReceived int

	closed atomic.Bool
	lock   sync.Mutex

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
	if r.closed.Load() {
		return
	}
	// write seems to handle growing the buffer if needed
	r.lock.Lock()
	r.readBuffer.Write(chunk.Content)
	r.lock.Unlock()
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

func (r *baseStreamReader) handleEOFBeforeStreamClosed(err error) error {
	if err == io.EOF {
		if r.closed.Load() {
			return io.EOF
		}
		return EAGAIN
	}
	return err
}

func (r *baseStreamReader) Read(bytes []byte) (int, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	n, err := r.readBuffer.Read(bytes)
	r.maybeCallOnProgress(n)

	return n, r.handleEOFBeforeStreamClosed(err)
}

func (r *baseStreamReader) ReadByte() (byte, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	n, err := r.readBuffer.ReadByte()
	r.maybeCallOnProgress(1)

	return n, r.handleEOFBeforeStreamClosed(err)
}

func (r *baseStreamReader) ReadBytes(delim byte) ([]byte, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	n, err := r.readBuffer.ReadBytes(delim)
	r.maybeCallOnProgress(len(n))
	return n, r.handleEOFBeforeStreamClosed(err)
}

func (r *baseStreamReader) close() {
	if !r.closed.Load() {
		r.closed.Store(true)
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
	r.lock.Lock()
	defer r.lock.Unlock()

	n, size, err := r.readBuffer.ReadRune()
	r.maybeCallOnProgress(size)
	return n, size, r.handleEOFBeforeStreamClosed(err)
}

func (r *TextStreamReader) ReadString(delim byte) (string, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	n, err := r.readBuffer.ReadString(delim)
	r.maybeCallOnProgress(len(n))
	return n, r.handleEOFBeforeStreamClosed(err)
}

func (r *TextStreamReader) ReadAll() string {
	// wait for the stream to be closed
	for {
		if r.closed.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Now that the stream is closed, read all data
	r.lock.Lock()
	defer r.lock.Unlock()

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
	for {
		if r.closed.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Now that the stream is closed, read all data
	r.lock.Lock()
	defer r.lock.Unlock()

	n := r.readBuffer.Bytes()
	r.maybeCallOnProgress(len(n))
	return n
}

type TextStreamHandler func(reader *TextStreamReader, participantIdentity string)

type ByteStreamHandler func(reader *ByteStreamReader, participantIdentity string)
