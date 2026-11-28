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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/twitchtv/twirp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/xtwirp"
)

func TestSecureInputErrorFromServerReply(t *testing.T) {
	want := livekit.SecureInputErrorReason_SECURE_INPUT_ERROR_CALL_NOT_ANSWERED
	st, err := status.New(codes.FailedPrecondition, "refused").WithDetails(&livekit.SecureInputError{Reason: want})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = twirp.WriteError(w, xtwirp.ToError(st.Err()))
	}))
	defer srv.Close()

	_, err = NewSIPClient(srv.URL, "key", "secret").StartSIPSecureInput(context.Background(), &livekit.StartSIPSecureInputRequest{SipCallId: "SCL_abc123"})
	require.Equal(t, want, SecureInputErrorFrom(err).GetReason())
}
