package main

import (
	"fmt"

	"github.com/gorilla/websocket"
	"github.com/livekit/media-sdk"
	"github.com/livekit/protocol/logger"
)

type WsManager struct {
	conn *websocket.Conn
	url  string
	cb   *WsManagerCallbacks
}

type WsManagerCallbacks struct {
	OnAudioReceived func(audio media.PCM16Sample)
}

func NewWsManager(cb *WsManagerCallbacks) (*WsManager, error) {
	conn, url, err := connectToWSS()
	if err != nil {
		return nil, err
	}

	ws := &WsManager{
		conn: conn,
		url:  url,
		cb:   cb,
	}

	go ws.readMessages()
	return ws, nil
}

func (m *WsManager) Url() string {
	return m.url
}

func (m *WsManager) SendMessage(messageType string, key string, value string) error {
	message := fmt.Sprintf(`{"type": "%s", "%s": "%s"}`, messageType, key, value)
	err := m.conn.WriteMessage(websocket.TextMessage, []byte(message))
	if err != nil {
		return err
	}

	return nil
}

func (m *WsManager) readMessages() {
	for {
		messageType, message, err := m.conn.ReadMessage()
		if err != nil {
			logger.Errorw("Error reading message", err)
			m.Close()
			break
		}

		fmt.Printf("Received message type %d: %s\n", messageType, string(message))
		handleMessage(string(message), m.cb)
	}
}

func (m *WsManager) Close() error {
	return m.conn.Close()
}
