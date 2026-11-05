// Copyright 2026 LiveKit, Inc.
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
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/server-sdk-go/v2/signalling"
)

type countingDialer struct{ calls atomic.Int32 }

func (d *countingDialer) Dial(network, addr string) (net.Conn, error) {
	d.calls.Inc()
	return net.Dial(network, addr)
}

func TestICEProxyDialerRelaysThroughTURN(t *testing.T) {
	// httptest's certificate is valid for 127.0.0.1.
	certSrv := httptest.NewTLSServer(nil)
	certSrv.Close()

	for _, scheme := range []string{"turns", "turn"} {
		t.Run(scheme, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			if scheme == "turns" {
				ln = tls.NewListener(ln, &tls.Config{Certificates: certSrv.TLS.Certificates})
			}
			srv, err := turn.NewServer(turn.ServerConfig{
				Realm: "test",
				AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
					return ra.Username, turn.GenerateAuthKey(ra.Username, ra.Realm, "pass"), true
				},
				ListenerConfigs: []turn.ListenerConfig{{
					Listener:              ln,
					RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.IPv4(127, 0, 0, 1), Address: "127.0.0.1"},
				}},
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = srv.Close() })

			dialer := &countingDialer{}
			e := NewRTCEngine(false, nil, func() string { return "" }, nil)
			t.Cleanup(e.Close)
			e.iceProxyDialer.rootCAs = certSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
			e.setIncomingRequestParams(context.Background(), "", "", &signalling.ConnectParams{
				ICEProxyDialer:     dialer,
				ICETransportPolicy: webrtc.ICETransportPolicyRelay,
			}, true)
			require.NoError(t, e.configure([]*livekit.ICEServer{{
				Urls:       []string{fmt.Sprintf("%s:%s?transport=tcp", scheme, ln.Addr())},
				Username:   "user",
				Credential: "pass",
			}}, nil, nil))

			pc := e.publisher.pc
			offer, err := pc.CreateOffer(nil)
			require.NoError(t, err)
			gathered := webrtc.GatheringCompletePromise(pc)
			require.NoError(t, pc.SetLocalDescription(offer))
			<-gathered

			require.Contains(t, pc.LocalDescription().SDP, "typ relay")
			require.Positive(t, dialer.calls.Load())
		})
	}
}
