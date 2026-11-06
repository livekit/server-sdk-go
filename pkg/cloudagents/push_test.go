package cloudagents

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/logger"
)

func TestGetPushTargetSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"proxy_host":"agents.livekit.io","name":"livekit","tag":"v1"}`)
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		logger:     logger.GetLogger(),
		apiKey:     "test-api-key",
		apiSecret:  "test-api-secret",
		agentsURL:  server.URL,
	}

	target, err := client.GetPushTarget(context.Background(), "test-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.ProxyHost != "agents.livekit.io" || target.Name != "livekit" || target.Tag != "v1" {
		t.Fatalf("unexpected push target: %+v", target)
	}
}

func TestGetPushTargetNonOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "oops")
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		logger:     logger.GetLogger(),
		apiKey:     "test-api-key",
		apiSecret:  "test-api-secret",
		agentsURL:  server.URL,
	}

	_, err := client.GetPushTarget(context.Background(), "test-agent")
	if err == nil {
		t.Fatal("expected error when push-target returns non-OK status")
	}
	if !strings.Contains(err.Error(), "push-target returned 400") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGetPushTargetDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "invalid-json")
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		logger:     logger.GetLogger(),
		apiKey:     "test-api-key",
		apiSecret:  "test-api-secret",
		agentsURL:  server.URL,
	}

	_, err := client.GetPushTarget(context.Background(), "test-agent")
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "failed to decode push-target response") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func checkRegistryAuth(t *testing.T, header http.Header) {
	t.Helper()
	token, ok := strings.CutPrefix(header.Get("Authorization"), "Bearer ")
	if !ok {
		t.Fatalf("push request has no bearer token: %q", header.Get("Authorization"))
	}
	v, err := auth.ParseAPIToken(token)
	if err != nil {
		t.Fatalf("unexpected error parsing token: %v", err)
	}
	if v.APIKey() != "test-api-key" {
		t.Fatalf("token signed with %q, want test-api-key", v.APIKey())
	}
	if _, _, err := v.Verify("test-api-secret"); err != nil {
		t.Fatalf("token does not verify with the client's secret: %v", err)
	}
}

func TestRegistryTransportUsesHTTPClientTransport(t *testing.T) {
	var seen *http.Request
	client := &Client{
		httpClient: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seen = req
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
		})},
		logger:    logger.GetLogger(),
		apiKey:    "test-api-key",
		apiSecret: "test-api-secret",
	}

	req, err := http.NewRequest(http.MethodGet, "http://registry.test/v2/", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := client.NewRegistryTransport(map[string]string{"key": "value"}).RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if seen == nil {
		t.Fatal("the HTTP client's transport did not see the push request")
	}
	checkRegistryAuth(t, seen.Header)
	if seen.Header.Get(attributesHeader) == "" {
		t.Fatal("push request has no attributes header")
	}
}

func TestRegistryTransportDefaultTransport(t *testing.T) {
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
	}))
	defer server.Close()

	client := &Client{
		httpClient: &http.Client{},
		logger:     logger.GetLogger(),
		apiKey:     "test-api-key",
		apiSecret:  "test-api-secret",
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v2/", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := client.NewRegistryTransport(nil).RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	checkRegistryAuth(t, header)
}
