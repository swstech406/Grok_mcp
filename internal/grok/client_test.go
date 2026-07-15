package grok

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/logx"
)

func TestNewHTTPClientWithProxyUsesExplicitProxy(t *testing.T) {
	client, err := newHTTPClientWithProxy(time.Second, " http://127.0.0.1:7890 ", true)
	if err != nil {
		t.Fatalf("newHTTPClientWithProxy failed: %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}

	request := &http.Request{URL: mustParseURL(t, "https://api.example.test/v1/responses")}
	actualProxyURL, err := transport.Proxy(request)
	if err != nil {
		t.Fatalf("resolve proxy: %v", err)
	}
	if actualProxyURL == nil || actualProxyURL.String() != "http://127.0.0.1:7890" {
		t.Fatalf("expected explicit proxy URL, got %v", actualProxyURL)
	}
}

func TestNewHTTPClientWithProxyFallsBackToEnvironment(t *testing.T) {
	client, err := newHTTPClientWithProxy(time.Second, "", false)
	if err != nil {
		t.Fatalf("newHTTPClientWithProxy failed: %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatalf("expected environment proxy resolver, got nil")
	}

	actualProxyFunctionPointer := reflect.ValueOf(transport.Proxy).Pointer()
	expectedProxyFunctionPointer := reflect.ValueOf(http.ProxyFromEnvironment).Pointer()
	if actualProxyFunctionPointer != expectedProxyFunctionPointer {
		t.Fatalf("expected http.ProxyFromEnvironment fallback")
	}
}

func TestNewHTTPClientWithProxyConfiguresUpstreamIdleConnectionPool(t *testing.T) {
	client, err := newHTTPClientWithProxy(time.Second, "", false)
	if err != nil {
		t.Fatalf("newHTTPClientWithProxy failed: %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.MaxIdleConns != upstreamIdleConnectionPoolSize {
		t.Fatalf("expected MaxIdleConns %d, got %d", upstreamIdleConnectionPoolSize, transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != upstreamIdleConnectionPoolSize {
		t.Fatalf("expected MaxIdleConnsPerHost %d, got %d", upstreamIdleConnectionPoolSize, transport.MaxIdleConnsPerHost)
	}
}

func TestNewHTTPClientWithProxyUsesPhaseTimeoutsWithoutSSELifecycleTimeout(t *testing.T) {
	phaseTimeout := 40 * time.Millisecond
	client, err := newHTTPClientWithProxy(phaseTimeout, "", false)
	if err != nil {
		t.Fatalf("newHTTPClientWithProxy failed: %v", err)
	}

	if client.Timeout != 0 {
		t.Fatalf("expected no total HTTP client timeout, got %v", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.DialContext == nil {
		t.Fatal("expected connection timeout dialer")
	}
	if transport.TLSHandshakeTimeout != phaseTimeout {
		t.Fatalf("expected TLS handshake timeout %v, got %v", phaseTimeout, transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != phaseTimeout {
		t.Fatalf("expected response header timeout %v, got %v", phaseTimeout, transport.ResponseHeaderTimeout)
	}
}

func TestHTTPClientAllowsSSEBodyToOutlivePhaseTimeout(t *testing.T) {
	phaseTimeout := 40 * time.Millisecond
	streamDelay := 120 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/event-stream")
		responseWriter.WriteHeader(http.StatusOK)
		responseWriter.(http.Flusher).Flush()

		time.Sleep(streamDelay)
		_, _ = io.WriteString(responseWriter, "data: completed\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClientWithProxy(phaseTimeout, "", false)
	if err != nil {
		t.Fatalf("newHTTPClientWithProxy failed: %v", err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request failed after response headers: %v", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read delayed SSE body: %v", err)
	}
	if string(responseBody) != "data: completed\n\n" {
		t.Fatalf("unexpected response body: %q", responseBody)
	}
}

func TestHTTPClientLimitsResponseHeaderWait(t *testing.T) {
	phaseTimeout := 40 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		time.Sleep(120 * time.Millisecond)
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := newHTTPClientWithProxy(phaseTimeout, "", false)
	if err != nil {
		t.Fatalf("newHTTPClientWithProxy failed: %v", err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("expected response header timeout")
	}
	if !strings.Contains(err.Error(), "timeout awaiting response headers") {
		t.Fatalf("expected response header timeout error, got %v", err)
	}
}

func TestNewHTTPClientWithProxyRejectsEnabledProxyWithoutURL(t *testing.T) {
	_, err := newHTTPClientWithProxy(time.Second, " ", true)
	if err == nil || !strings.Contains(err.Error(), "proxy URL is required when proxy is enabled") {
		t.Fatalf("expected missing proxy URL error, got %v", err)
	}
}

func TestApplyServerSettingsUpdatesSharedDebugState(t *testing.T) {
	configuration := &config.Config{
		CPABaseURL:       "https://api.example.test",
		CPAAPIKey:        "test-key",
		UpstreamProtocol: config.UpstreamProtocolResponses,
		Model:            "grok-4.3",
		Timeout:          time.Second,
		RegistrationMode: "free",
		Debug:            false,
	}
	debugState := logx.NewDebugState(false)
	client, err := NewClientWithServerSettings(configuration.ServerSettings(), debugState)
	if err != nil {
		t.Fatalf("NewClientWithServerSettings failed: %v", err)
	}

	settings := configuration.ServerSettings()
	settings.Debug = true
	if err := client.ApplyServerSettings(settings); err != nil {
		t.Fatalf("enable debug: %v", err)
	}
	if !debugState.Enabled() {
		t.Fatal("expected shared debug state to be enabled")
	}

	settings.Debug = false
	if err := client.ApplyServerSettings(settings); err != nil {
		t.Fatalf("disable debug: %v", err)
	}
	if debugState.Enabled() {
		t.Fatal("expected shared debug state to be disabled")
	}

	invalidSettings := settings
	invalidSettings.Debug = true
	invalidSettings.ProxyEnabled = true
	invalidSettings.ProxyURL = ""
	if err := client.ApplyServerSettings(invalidSettings); err == nil {
		t.Fatal("expected invalid proxy settings to fail")
	}
	if debugState.Enabled() {
		t.Fatal("failed settings update must not change shared debug state")
	}
}

func TestApplyServerSettingsClosesReplacedClientIdleConnections(t *testing.T) {
	configuration := &config.Config{
		CPABaseURL:       "https://api.example.test",
		CPAAPIKey:        "test-key",
		UpstreamProtocol: config.UpstreamProtocolResponses,
		Model:            "grok-4.3",
		Timeout:          time.Second,
		RegistrationMode: "free",
	}
	client := newTestClient(t, configuration)
	replacedTransport := &closeIdleTrackingTransport{}

	client.mu.Lock()
	client.httpClient = &http.Client{Transport: replacedTransport}
	client.mu.Unlock()

	settings := configuration.ServerSettings()
	settings.Model = "grok-4.4"
	if err := client.ApplyServerSettings(settings); err != nil {
		t.Fatalf("ApplyServerSettings failed: %v", err)
	}
	if replacedTransport.closeIdleConnectionCalls != 1 {
		t.Fatalf("expected replaced client idle connections to close once, got %d", replacedTransport.closeIdleConnectionCalls)
	}
}

type closeIdleTrackingTransport struct {
	http.RoundTripper
	closeIdleConnectionCalls int
}

func (transport *closeIdleTrackingTransport) CloseIdleConnections() {
	transport.closeIdleConnectionCalls++
}

func newTestClient(t *testing.T, configuration *config.Config) *Client {
	t.Helper()
	client, err := NewClientWithServerSettings(configuration.ServerSettings(), nil)
	if err != nil {
		t.Fatalf("NewClientWithServerSettings failed: %v", err)
	}
	return client
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	return parsedURL
}
