package lksdk

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"

	"github.com/livekit/protocol/livekit"
)

type recordingProxyDialer struct {
	calls atomic.Int32
}

func (d *recordingProxyDialer) Dial(_ string, addr string) (net.Conn, error) {
	d.calls.Add(1)
	return nil, fmt.Errorf("refusing proxy dial to %s", addr)
}

func TestICEProxyDialerFactoryAppliesToBothTransports(t *testing.T) {
	engine := NewRTCEngine(false, nil, func() string { return "" }, nil)
	dialer := &recordingProxyDialer{}
	var received []*livekit.ICEServer
	engine.SetICEProxyDialerFactory(func(iceServers []*livekit.ICEServer) proxy.Dialer {
		received = iceServers
		return dialer
	})

	iceServers := []*livekit.ICEServer{{
		Urls:       []string{"turns:127.0.0.1:65500?transport=tcp"},
		Username:   "user",
		Credential: "password",
	}}
	require.NoError(t, engine.configure(iceServers, nil, nil))
	require.Equal(t, iceServers, received)

	for _, transport := range []*PCTransport{engine.publisher, engine.subscriber} {
		offer, err := transport.pc.CreateOffer(nil)
		require.NoError(t, err)
		require.NoError(t, transport.pc.SetLocalDescription(offer))
	}

	require.Eventually(t, func() bool {
		return dialer.calls.Load() >= 2
	}, 5*time.Second, 50*time.Millisecond)
}
