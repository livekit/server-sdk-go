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
	"crypto/tls"
	"crypto/x509"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/pion/stun/v4"
	"golang.org/x/net/proxy"

	"github.com/livekit/protocol/livekit"
)

const turnsProxyHandshakeTimeout = 10 * time.Second

// turnsProxyDialer dials TURN servers through a caller-supplied proxy and adds
// the TLS layer for turns: servers, which pion skips when a proxy dialer is set.
type turnsProxyDialer struct {
	mu          sync.RWMutex
	base        proxy.Dialer
	serverNames map[string]string // turns: dial address -> TLS server name
	rootCAs     *x509.CertPool    // nil uses the system roots
}

func (d *turnsProxyDialer) setBase(base proxy.Dialer) {
	d.mu.Lock()
	d.base = base
	d.mu.Unlock()
}

func (d *turnsProxyDialer) setICEServers(iceServers []*livekit.ICEServer) {
	serverNames := make(map[string]string)
	for _, s := range iceServers {
		for _, raw := range s.Urls {
			u, err := stun.ParseURI(raw)
			if err != nil || u.Scheme != stun.SchemeTypeTURNS {
				continue
			}
			// Same address format pion passes to Dial.
			serverNames[net.JoinHostPort(u.Host, strconv.Itoa(u.Port))] = u.Host
		}
	}
	d.mu.Lock()
	d.serverNames = serverNames
	d.mu.Unlock()
}

func (d *turnsProxyDialer) Dial(network, addr string) (net.Conn, error) {
	d.mu.RLock()
	base, rootCAs := d.base, d.rootCAs
	serverName, isTURNS := d.serverNames[addr]
	d.mu.RUnlock()

	conn, err := base.Dial(network, addr)
	if err != nil || !isTURNS {
		return conn, err
	}

	tlsConn := tls.Client(conn, &tls.Config{ServerName: serverName, RootCAs: rootCAs})
	_ = conn.SetDeadline(time.Now().Add(turnsProxyHandshakeTimeout))
	if err = tlsConn.Handshake(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return tlsConn, nil
}
