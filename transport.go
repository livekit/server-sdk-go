package sdk

import (
	"sync"
	"time"

	"github.com/bep/debounce"
	"github.com/pion/webrtc/v3"
)

const (
	negotiationFrequency = 150 * time.Millisecond
)

// PCTransport is a wrapper around PeerConnection, with some helper methods
type PCTransport struct {
	pc *webrtc.PeerConnection
	me *webrtc.MediaEngine

	lock               sync.Mutex
	pendingCandidates  []webrtc.ICECandidateInit
	debouncedNegotiate func(func())
	onNegotiation      func()
}

func NewPCTransport(iceServers []webrtc.ICEServer) (*PCTransport, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return nil, err
	}

	t := &PCTransport{
		pc:                 pc,
		debouncedNegotiate: debounce.New(negotiationFrequency),
	}

	t.pc.OnNegotiationNeeded(t.negotiate)

	return t, nil
}

func (t *PCTransport) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	if t.pc.RemoteDescription() == nil {
		t.lock.Lock()
		t.pendingCandidates = append(t.pendingCandidates, candidate)
		t.lock.Unlock()
		return nil
	}

	return t.pc.AddICECandidate(candidate)
}

func (t *PCTransport) PeerConnection() *webrtc.PeerConnection {
	return t.pc
}

func (t *PCTransport) IsConnected() bool {
	return t.pc.ICEConnectionState() == webrtc.ICEConnectionStateConnected
}

func (t *PCTransport) Close() {
	t.pc.Close()
}

func (t *PCTransport) SetRemoteDescription(sd webrtc.SessionDescription) error {
	if err := t.pc.SetRemoteDescription(sd); err != nil {
		return err
	}

	t.lock.Lock()
	for _, c := range t.pendingCandidates {
		if err := t.pc.AddICECandidate(c); err != nil {
			return err
		}
	}
	t.pendingCandidates = nil
	t.lock.Unlock()

	return nil
}

func (t *PCTransport) OnNegotiationNeeded(f func()) {
	t.onNegotiation = f
}

func (t *PCTransport) negotiate() {
	t.debouncedNegotiate(func() {
		if t.onNegotiation != nil {
			t.onNegotiation()
		}
	})
}
