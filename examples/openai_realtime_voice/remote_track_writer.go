package main

import (
	"errors"

	"go.uber.org/atomic"

	"github.com/livekit/media-sdk"
)

var ErrClosed = errors.New("writer is closed")

type RemoteTrackWriter struct {
	ws     *WsManager
	closed atomic.Bool
}

func NewRemoteTrackWriter(ws *WsManager) *RemoteTrackWriter {
	return &RemoteTrackWriter{
		ws: ws,
	}
}

func (w *RemoteTrackWriter) WriteSample(sample media.PCM16Sample) error {
	if w.closed.Load() {
		return ErrClosed
	}

	return sendAudioChunk(w.ws, sample)
}

func (w *RemoteTrackWriter) Close() error {
	w.closed.Swap(true)
	return nil
}
