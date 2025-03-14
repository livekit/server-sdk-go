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
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pion/webrtc/v4"

	"github.com/livekit/protocol/livekit"
)

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
	if iceservers == nil {
		return nil
	}
	servers := make([]webrtc.ICEServer, 0, len(iceservers))
	for _, server := range iceservers {
		servers = append(servers, webrtc.ICEServer{
			URLs:       server.Urls,
			Username:   server.Username,
			Credential: server.Credential,
		})
	}
	return servers
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

func byteLength(str string) int {
	return len([]byte(str))
}

func truncateBytes(str string, maxBytes int) string {
	if byteLength(str) <= maxBytes {
		return str
	} else {
		byteStr := []byte(str)
		return string(byteStr[:maxBytes])
	}
}

func compareVersions(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	k := min(len(parts1), len(parts2))

	for i := range k {
		p1, _ := strconv.Atoi(parts1[i])
		p2, _ := strconv.Atoi(parts2[i])

		if p1 < p2 {
			return -1
		} else if parts1[i] > parts2[i] {
			return 1
		} else if (i == k-1) && (p1 == p2) {
			return 0
		}
	}

	if len(parts1) < len(parts2) {
		return -1
	} else if len(parts1) > len(parts2) {
		return 1
	} else {
		return 0
	}
}
