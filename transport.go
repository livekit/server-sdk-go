// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lksdk

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bep/debounce"
	"github.com/pion/dtls/v2"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"

	lksdp "github.com/livekit/protocol/sdp"
	sdkinterceptor "github.com/livekit/server-sdk-go/pkg/interceptor"
)

const (
	negotiationFrequency = 150 * time.Millisecond
)

// PCTransport is a wrapper around PeerConnection, with some helper methods
type PCTransport struct {
	pc *webrtc.PeerConnection

	lock                      sync.Mutex
	pendingCandidates         []webrtc.ICECandidateInit
	debouncedNegotiate        func(func())
	renegotiate               bool
	currentOfferIceCredential string
	pendingRestartIceOffer    *webrtc.SessionDescription
	restartAfterGathering     bool
	nackGenerator             *sdkinterceptor.NackGeneratorInterceptorFactory

	onRemoteDescriptionSettled func() error

	OnOffer func(description webrtc.SessionDescription)
}

func NewPCTransport(configuration webrtc.Configuration) (*PCTransport, error) {
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

	// nack interceptor
	generator := &sdkinterceptor.NackGeneratorInterceptorFactory{}
	responder, err := nack.NewResponderInterceptor()
	if err != nil {
		return nil, err
	}

	m.RegisterFeedback(webrtc.RTCPFeedback{Type: "nack"}, webrtc.RTPCodecTypeVideo)
	m.RegisterFeedback(webrtc.RTCPFeedback{Type: "nack", Parameter: "pli"}, webrtc.RTPCodecTypeVideo)
	i.Add(responder)
	i.Add(generator)

	// rtcp report interceptor
	if err := webrtc.ConfigureRTCPReports(i); err != nil {
		return nil, err
	}

	// twcc interceptor
	if err := webrtc.ConfigureTWCCSender(m, i); err != nil {
		return nil, err
	}

	se := webrtc.SettingEngine{}
	se.SetSRTPProtectionProfiles(dtls.SRTP_AEAD_AES_128_GCM, dtls.SRTP_AES128_CM_HMAC_SHA1_80)

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se), webrtc.WithInterceptorRegistry(i))
	pc, err := api.NewPeerConnection(configuration)
	if err != nil {
		return nil, err
	}

	t := &PCTransport{
		pc:                 pc,
		debouncedNegotiate: debounce.New(negotiationFrequency),
		nackGenerator:      generator,
	}

	pc.OnICEGatheringStateChange(t.onICEGatheringStateChange)

	return t, nil
}

func (t *PCTransport) onICEGatheringStateChange(state webrtc.ICEGathererState) {
	if state != webrtc.ICEGathererStateComplete {
		return
	}

	go func() {
		t.lock.Lock()
		if t.restartAfterGathering {
			t.lock.Unlock()
			logger.Infow("restarting ICE after ICE gathering")
			if err := t.createAndSendOffer(&webrtc.OfferOptions{ICERestart: true}); err != nil {
				logger.Errorw("could not restart ICE", err)
			}
		} else if t.pendingRestartIceOffer != nil {
			logger.Infow("accept remote restart ice offer after ICE gathering")
			offer := t.pendingRestartIceOffer
			t.pendingRestartIceOffer = nil
			t.lock.Unlock()
			if err := t.SetRemoteDescription(*offer); err != nil {
				logger.Errorw("could not accept remote restart ICE offer", err)
			}
		} else {
			t.lock.Unlock()
		}
	}()
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

func (t *PCTransport) SetRTT(rtt uint32) {
	if g := t.nackGenerator; g != nil {
		g.SetRTT(rtt)
	}
}

func (t *PCTransport) SetRemoteDescription(sd webrtc.SessionDescription) error {
	t.lock.Lock()

	var (
		iceCredential   string
		offerRestartICE bool
	)
	if sd.Type == webrtc.SDPTypeOffer {
		var err error
		iceCredential, offerRestartICE, err = t.isRemoteOfferRestartICE(sd)
		if err != nil {
			logger.Errorw("check remote offer restart ICE failed", err)
			t.lock.Unlock()
			return err
		}
	}

	if offerRestartICE && t.pc.ICEGatheringState() == webrtc.ICEGatheringStateGathering {
		logger.Infow("remote offer restart ice while ice gathering")
		t.pendingRestartIceOffer = &sd
		t.lock.Unlock()
		return nil
	}

	if err := t.pc.SetRemoteDescription(sd); err != nil {
		t.lock.Unlock()
		return err
	}

	if t.currentOfferIceCredential == "" || offerRestartICE {
		t.currentOfferIceCredential = iceCredential
	}

	for _, c := range t.pendingCandidates {
		if err := t.pc.AddICECandidate(c); err != nil {
			t.lock.Unlock()
			return err
		}
	}
	t.pendingCandidates = nil

	if t.renegotiate {
		t.renegotiate = false
		go t.createAndSendOffer(nil)
	}

	onRemoteDescriptionSettled := t.onRemoteDescriptionSettled
	t.lock.Unlock()

	if onRemoteDescriptionSettled != nil {
		return onRemoteDescriptionSettled()
	}
	return nil
}

func (t *PCTransport) OnRemoteDescriptionSettled(f func() error) {
	t.lock.Lock()
	t.onRemoteDescriptionSettled = f
	t.lock.Unlock()
}

func (t *PCTransport) GetSelectedCandidatePair() (*webrtc.ICECandidatePair, error) {
	sctp := t.pc.SCTP()
	if sctp == nil {
		return nil, errors.New("no SCTP")
	}

	dtlsTransport := sctp.Transport()
	if dtlsTransport == nil {
		return nil, errors.New("no DTLS transport")
	}

	iceTransport := dtlsTransport.ICETransport()
	if iceTransport == nil {
		return nil, errors.New("no ICE transport")
	}

	return iceTransport.GetSelectedCandidatePair()
}

func (t *PCTransport) isRemoteOfferRestartICE(sd webrtc.SessionDescription) (string, bool, error) {
	parsed, err := sd.Unmarshal()
	if err != nil {
		return "", false, err
	}
	user, pwd, err := lksdp.ExtractICECredential(parsed)
	if err != nil {
		return "", false, err
	}

	credential := fmt.Sprintf("%s:%s", user, pwd)
	// ice credential changed, remote offer restart ice
	restartICE := t.currentOfferIceCredential != "" && t.currentOfferIceCredential != credential
	return credential, restartICE, nil
}

func (t *PCTransport) Negotiate() {
	t.debouncedNegotiate(func() {
		t.createAndSendOffer(nil)
	})
}

func (t *PCTransport) createAndSendOffer(options *webrtc.OfferOptions) error {
	if t.OnOffer == nil {
		return nil
	}
	t.lock.Lock()
	defer t.lock.Unlock()

	iceRestart := options != nil && options.ICERestart
	if iceRestart {
		if t.pc.ICEGatheringState() == webrtc.ICEGatheringStateGathering {
			t.restartAfterGathering = true
			return nil
		}
		logger.Debugw("restarting ICE")
	}
	if t.pc.SignalingState() == webrtc.SignalingStateHaveLocalOffer {
		if iceRestart {
			currentSD := t.pc.CurrentRemoteDescription()
			if currentSD != nil {
				if err := t.pc.SetRemoteDescription(*currentSD); err != nil {
					return err
				}
			}
		} else {
			t.renegotiate = true
			return nil
		}
	}

	logger.Debugw("starting to negotiate")
	offer, err := t.pc.CreateOffer(options)
	logger.Debugw("create offer", "offer", offer.SDP)
	if err != nil {
		logger.Errorw("could not negotiate", err)
		return err
	}
	if err := t.pc.SetLocalDescription(offer); err != nil {
		logger.Errorw("could not set local description", err)
		return err
	}
	t.restartAfterGathering = false
	t.OnOffer(offer)
	return nil
}
