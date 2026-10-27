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

package signalling

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

var testCaps = []livekit.ClientInfo_Capability{
	livekit.ClientInfo_CAP_PACKET_TRAILER,
	livekit.ClientInfo_CAP_COMPRESSION_DEFLATE_RAW,
}

func TestCapabilitiesQueryParam(t *testing.T) {
	s := NewSignalling(SignallingParams{Logger: logger.GetLogger()})
	q, err := s.ConnectQueryParams("1.0.0", 17, &ConnectParams{Capabilities: testCaps}, nil, webrtc.SessionDescription{}, "")
	require.NoError(t, err)
	values, err := url.ParseQuery(q)
	require.NoError(t, err)
	require.Equal(t, "CAP_PACKET_TRAILER,CAP_COMPRESSION_DEFLATE_RAW", values.Get("capabilities"))

	q, err = s.ConnectQueryParams("1.0.0", 17, &ConnectParams{}, nil, webrtc.SessionDescription{}, "")
	require.NoError(t, err)
	require.False(t, strings.Contains(q, "capabilities"))
}

func TestCapabilitiesJoinRequest(t *testing.T) {
	s := NewSignallingJoinRequest(SignallingJoinRequestParams{Logger: logger.GetLogger()})
	q, err := s.ConnectQueryParams("1.0.0", 17, &ConnectParams{Capabilities: testCaps}, nil, webrtc.SessionDescription{}, "")
	require.NoError(t, err)
	values, err := url.ParseQuery(q)
	require.NoError(t, err)
	data, err := base64.URLEncoding.DecodeString(values.Get("join_request"))
	require.NoError(t, err)

	var wrapped livekit.WrappedJoinRequest
	require.NoError(t, proto.Unmarshal(data, &wrapped))
	r, err := gzip.NewReader(bytes.NewReader(wrapped.JoinRequest))
	require.NoError(t, err)
	marshalled, err := io.ReadAll(r)
	require.NoError(t, err)
	var req livekit.JoinRequest
	require.NoError(t, proto.Unmarshal(marshalled, &req))
	require.Equal(t, testCaps, req.ClientInfo.Capabilities)
}
