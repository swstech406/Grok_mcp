package grok

import (
	"net/http"
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
