package cloudagents

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	lkproto "github.com/livekit/protocol/livekit"
)

func TestGetAgentsURL(t *testing.T) {
	tests := []struct {
		name         string
		projectURL   string
		overrideURL  string
		serverRegion string
		expected     string
	}{
		{
			name:       "standard https project URL",
			projectURL: "https://my-project.livekit.cloud",
			expected:   "https://agents.livekit.cloud",
		},
		{
			name:       "wss project URL",
			projectURL: "wss://my-project.livekit.cloud",
			expected:   "https://agents.livekit.cloud",
		},
		{
			name:       "subdomain containing localhost",
			projectURL: "wss://andrewnitu-localhost-project.livekit.cloud",
			expected:   "https://agents.livekit.cloud",
		},
		{
			name:       "subdomain containing 127",
			projectURL: "https://test-127-app.livekit.cloud",
			expected:   "https://agents.livekit.cloud",
		},
		{
			name:         "with server region",
			projectURL:   "https://my-project.livekit.cloud",
			serverRegion: "osbxash1a",
			expected:     "https://osbxash1a.agents.livekit.cloud",
		},
		{
			name:         "wss with server region",
			projectURL:   "wss://my-project.livekit.cloud",
			serverRegion: "osbxmum1a",
			expected:     "https://osbxmum1a.agents.livekit.cloud",
		},
		{
			name:       "localhost development URL",
			projectURL: "http://localhost:7880",
			expected:   "http://localhost:7880",
		},
		{
			name:       "127.0.0.1 development URL",
			projectURL: "http://127.0.0.1:7880",
			expected:   "http://127.0.0.1:7880",
		},
		{
			name:       "staging project URL",
			projectURL: "https://my-project.staging.livekit.cloud",
			expected:   "https://agents.staging.livekit.cloud",
		},
		{
			name:        "WithAgentsURL",
			projectURL:  "https://my-project.livekit.cloud",
			overrideURL: "https://agents.staging.livekit.cloud",
			expected:    "https://agents.staging.livekit.cloud",
		},
		{
			name:         "WithAgentsURL with server region",
			projectURL:   "https://my-project.livekit.cloud",
			overrideURL:  "https://agents.staging.livekit.cloud",
			serverRegion: "osbxash1a",
			expected:     "https://osbxash1a.agents.staging.livekit.cloud",
		},
		{
			name:        "local WithAgentsURL",
			projectURL:  "https://my-project.livekit.cloud",
			overrideURL: "http://127.0.0.1:7880",
			expected:    "http://127.0.0.1:7880",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{projectURL: tt.projectURL, overrideURL: tt.overrideURL}
			result := c.getAgentsURL(tt.serverRegion)
			require.Equal(t, tt.expected, result)
		})
	}
}

// Twirp calls and the HTTP routes both go to a local server as it is.
func TestLocalAgentsURL(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/twirp/livekit.CloudAgent/ListAgents" {
			w.Header().Set("Content-Type", "application/protobuf")
		}
	}))
	defer srv.Close()

	for name, opts := range map[string][]ClientOption{
		"project URL":   {WithProject(srv.URL, "key", "secret")},
		"WithAgentsURL": {WithProject("https://my-project.livekit.cloud", "key", "secret"), WithAgentsURL(srv.URL)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LK_AGENTS_URL", "")
			paths = nil
			c, err := New(opts...)
			require.NoError(t, err)

			_, err = c.ListAgents(context.Background(), &lkproto.ListAgentsRequest{})
			require.NoError(t, err)
			require.NoError(t, c.StreamLogs(context.Background(), "deploy", "CA_test", "", &bytes.Buffer{}, ""))
			require.Equal(t, []string{"/twirp/livekit.CloudAgent/ListAgents", "/logs"}, paths)
		})
	}
}
