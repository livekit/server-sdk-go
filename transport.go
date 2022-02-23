package lksdk

import (
	"sync"
	"time"

	"github.com/bep/debounce"
	"github.com/pion/interceptor"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

const (
	negotiationFrequency = 150 * time.Millisecond
)

// PCTransport is a wrapper around PeerConnection, with some helper methods
type PCTransport struct {
	pc *webrtc.PeerConnection

	lock               sync.Mutex
	pendingCandidates  []webrtc.ICECandidateInit
	debouncedNegotiate func(func())
	renegotiate        bool

	OnOffer func(description webrtc.SessionDescription)
}

func NewPCTransport(iceServers []webrtc.ICEServer) (*PCTransport, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	audioLevelExtension := webrtc.RTPHeaderExtensionCapability{URI: sdp.AudioLevelURI}
	if err := m.RegisterHeaderExtension(audioLevelExtension, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	sdesMidExtension := webrtc.RTPHeaderExtensionCapability{URI: sdp.SDESMidURI}
	if err := m.RegisterHeaderExtension(sdesMidExtension, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	sdesRtpStreamIdExtension := webrtc.RTPHeaderExtensionCapability{URI: sdp.SDESRTPStreamIDURI}
	if err := m.RegisterHeaderExtension(sdesRtpStreamIdExtension, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}

	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return nil, err
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return nil, err
	}

	t := &PCTransport{
		pc:                 pc,
		debouncedNegotiate: debounce.New(negotiationFrequency),
	}

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

func (t *PCTransport) Close() error {
	return t.pc.Close()
}

func (t *PCTransport) SetRemoteDescription(sd webrtc.SessionDescription) error {
	if err := t.pc.SetRemoteDescription(sd); err != nil {
		return err
	}

	t.lock.Lock()
	defer t.lock.Unlock()
	for _, c := range t.pendingCandidates {
		if err := t.pc.AddICECandidate(c); err != nil {
			return err
		}
	}
	t.pendingCandidates = nil

	if t.renegotiate {
		t.renegotiate = false
		go t.createAndSendOffer(nil)
	}
	return nil
}

func (t *PCTransport) Negotiate() {
	t.debouncedNegotiate(func() {
		t.createAndSendOffer(nil)
	})
}

func (t *PCTransport) createAndSendOffer(options *webrtc.OfferOptions) {
	if t.OnOffer == nil {
		return
	}
	t.lock.Lock()
	defer t.lock.Unlock()

	// TODO: does not support ice restart yet
	//if options.ICERestart {
	//	logger.V(1).Info("restarting ICE")
	//}
	if t.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
		t.renegotiate = true
		return
	}

	logger.V(1).Info("starting to negotiate")
	offer, err := t.pc.CreateOffer(options)
	logger.V(1).Info("create offer", "offer", offer.SDP)
	if err != nil {
		logger.Error(err, "could not negotiate")
	}
	if err := t.pc.SetLocalDescription(offer); err != nil {
		logger.Error(err, "could not set local description")
	}
	t.OnOffer(offer)
}
