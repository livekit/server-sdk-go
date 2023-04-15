package lksdk

import (
	"encoding/json"
	"strings"

	"github.com/pion/webrtc/v3"
	"github.com/thoas/go-funk"

	"errors"
	"github.com/livekit/protocol/livekit"
	"time"
)

var (
	ErrInvalidOpusPacket = errors.New("invalid opus packet")
)

// Parse the duration of a an OpusPacket
// https://www.rfc-editor.org/rfc/rfc6716#section-3.1
func ParseOpusPacketDuration(data []byte) (time.Duration, error) {
	durations := [32]uint64{
		480, 960, 1920, 2880, // Silk-Only
		480, 960, 1920, 2880, // Silk-Only
		480, 960, 1920, 2880, // Silk-Only
		480, 960, // Hybrid
		480, 960, // Hybrid
		120, 240, 480, 960, // Celt-Only
		120, 240, 480, 960, // Celt-Only
		120, 240, 480, 960, // Celt-Only
		120, 240, 480, 960, // Celt-Only
	}

	if len(data) < 1 {
		return 0, ErrInvalidOpusPacket
	}

	toc := data[0]
	var nframes int
	switch toc & 3 {
	case 0:
		nframes = 1
	case 1:
		nframes = 2
	case 2:
		nframes = 2
	case 3:
		if len(data) < 2 {
			return 0, ErrInvalidOpusPacket
		}
		nframes = int(data[1] & 63)
	}

	frameDuration := int64(durations[toc>>3])
	duration := int64(nframes * int(frameDuration))
	if duration > 5760 { // 120ms
		return 0, ErrInvalidOpusPacket
	}

	ms := duration * 1000 / 48000
	return time.Duration(ms) * time.Millisecond, nil
}

func ToProtoSessionDescription(sd webrtc.SessionDescription) *livekit.SessionDescription {
	return &livekit.SessionDescription{
		Type: sd.Type.String(),
		Sdp:  sd.SDP,
	}
}

func FromProtoSessionDescription(sd *livekit.SessionDescription) webrtc.SessionDescription {
	var sdType webrtc.SDPType
	switch sd.Type {
	case webrtc.SDPTypeOffer.String():
		sdType = webrtc.SDPTypeOffer
	case webrtc.SDPTypeAnswer.String():
		sdType = webrtc.SDPTypeAnswer
	case webrtc.SDPTypePranswer.String():
		sdType = webrtc.SDPTypePranswer
	case webrtc.SDPTypeRollback.String():
		sdType = webrtc.SDPTypeRollback
	}
	return webrtc.SessionDescription{
		Type: sdType,
		SDP:  sd.Sdp,
	}
}

func ToProtoTrickle(candidateInit webrtc.ICECandidateInit, target livekit.SignalTarget) *livekit.TrickleRequest {
	data, _ := json.Marshal(candidateInit)
	return &livekit.TrickleRequest{
		CandidateInit: string(data),
		Target:        target,
	}
}

func FromProtoTrickle(trickle *livekit.TrickleRequest) webrtc.ICECandidateInit {
	ci := webrtc.ICECandidateInit{}
	json.Unmarshal([]byte(trickle.CandidateInit), &ci)
	return ci
}

func FromProtoIceServers(iceservers []*livekit.ICEServer) []webrtc.ICEServer {
	servers := funk.Map(iceservers, func(server *livekit.ICEServer) webrtc.ICEServer {
		return webrtc.ICEServer{
			URLs:       server.Urls,
			Username:   server.Username,
			Credential: server.Credential,
		}
	})
	return servers.([]webrtc.ICEServer)
}

func ToHttpURL(url string) string {
	if strings.HasPrefix(url, "ws") {
		return strings.Replace(url, "ws", "http", 1)
	}
	return url
}

func ToWebsocketURL(url string) string {
	if strings.HasPrefix(url, "http") {
		return strings.Replace(url, "http", "ws", 1)
	}
	return url
}

// -----------------------------------------------
