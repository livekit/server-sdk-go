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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/auth"
)

// authorizationHeaders returns the Authorization header values of the single
// request that call makes to a stub server.
func authorizationHeaders(t *testing.T, call func(c *SIPClient) error) []string {
	t.Helper()

	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal","msg":"stub"}`))
	}))
	defer srv.Close()

	c := newSIPClient(srv.URL, authBase{apiKey: testAPIKey, apiSecret: testAPISecret}, srv.Client())
	require.Error(t, call(c))
	return got
}

func TestSIPGetByIDsSendsOneAuthorizationHeader(t *testing.T) {
	ctx := context.Background()
	ids := []string{"ST_1"}

	calls := map[string]func(c *SIPClient) error{
		"GetSIPInboundTrunksByIDs": func(c *SIPClient) error {
			_, err := c.GetSIPInboundTrunksByIDs(ctx, ids)
			return err
		},
		"GetSIPOutboundTrunksByIDs": func(c *SIPClient) error {
			_, err := c.GetSIPOutboundTrunksByIDs(ctx, ids)
			return err
		},
		"GetSIPDispatchRulesByIDs": func(c *SIPClient) error {
			_, err := c.GetSIPDispatchRulesByIDs(ctx, ids)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			headers := authorizationHeaders(t, call)
			require.Len(t, headers, 1)

			verifier, err := auth.ParseAPIToken(headers[0][len("Bearer "):])
			require.NoError(t, err)
			require.Equal(t, testAPIKey, verifier.APIKey())
		})
	}
}
