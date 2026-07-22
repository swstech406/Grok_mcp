package http_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/app"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/panel"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func buildNetworkTrustHandler(
	testingInstance *testing.T,
	clientIPResolver *ratelimit.ClientIPResolver,
) http.Handler {
	testingInstance.Helper()

	userLimiter := ratelimit.NewUserLimiter()
	searchConcurrencyLimiter := ratelimit.NewSearchConcurrencyLimiter(1, 1)
	mcpIPLimiter := ratelimit.NewIPLimiterWithConfig(ratelimit.IPLimiterConfig{
		RequestsPerMinute: 1,
		ClientIPResolver:  clientIPResolver,
	})
	testingInstance.Cleanup(func() {
		mcpIPLimiter.Close()
		searchConcurrencyLimiter.Close()
		userLimiter.Close()
	})

	panelHandler := &panel.Handler{
		Store:     store.TestStore{},
		JWTSecret: "network-trust-test-jwt-secret-at-least-32-bytes",
		AuthProtector: panel.NewAuthProtector(panel.AuthProtectorConfig{
			ClientIPResolver:         clientIPResolver,
			LoginIPRequestsPerMinute: 1,
			LoginIPBurst:             1,
		}),
	}
	return app.BuildHTTPHandler(app.HTTPDependencies{
		Store:                    store.TestStore{},
		MCPServer:                mcp.NewServer(&mcp.Implementation{Name: "network-trust-test", Version: "test"}, nil),
		UserLimiter:              userLimiter,
		SearchConcurrencyLimiter: searchConcurrencyLimiter,
		MCPIPLimiter:             mcpIPLimiter,
		PanelHandler:             panelHandler,
	})
}

func TestAssembledDirectModeCannotBypassMCPOrPanelProtectionWithHeaders(t *testing.T) {
	handler := buildNetworkTrustHandler(t, ratelimit.NewClientIPResolver())

	for requestIndex := 0; requestIndex < 2; requestIndex++ {
		mcpRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		mcpRequest.RemoteAddr = "198.51.100.10:443"
		mcpRequest.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(requestIndex+1))
		mcpResponse := httptest.NewRecorder()
		handler.ServeHTTP(mcpResponse, mcpRequest)

		if requestIndex == 0 && mcpResponse.Code == http.StatusTooManyRequests {
			t.Fatalf("first MCP request was unexpectedly rate limited")
		}
		if requestIndex == 1 && mcpResponse.Code != http.StatusTooManyRequests {
			t.Fatalf("second MCP request status = %d, want %d", mcpResponse.Code, http.StatusTooManyRequests)
		}
	}

	for requestIndex := 0; requestIndex < 2; requestIndex++ {
		loginRequest := httptest.NewRequest(
			http.MethodPost,
			"/panel/v1/auth/login",
			bytes.NewBufferString(`{"username":"unknown","password":"password123"}`),
		)
		loginRequest.RemoteAddr = "198.51.100.20:443"
		loginRequest.Header.Set("Content-Type", "application/json")
		loginRequest.Header.Set("X-Real-IP", "203.0.113."+strconv.Itoa(requestIndex+1))
		loginResponse := httptest.NewRecorder()
		handler.ServeHTTP(loginResponse, loginRequest)

		if requestIndex == 0 && loginResponse.Code == http.StatusTooManyRequests {
			t.Fatalf("first panel login request was unexpectedly rate limited")
		}
		if requestIndex == 1 && loginResponse.Code != http.StatusTooManyRequests {
			t.Fatalf("second panel login request status = %d, want %d", loginResponse.Code, http.StatusTooManyRequests)
		}
	}
}

func TestAssembledTrustedProxyModeRejectsUntrustedAndHeaderlessPeers(t *testing.T) {
	resolver := ratelimit.NewClientIPResolverWithConfig(ratelimit.ClientIPResolverConfig{
		Mode:                 ratelimit.ClientIPModeTrustedProxy,
		TrustedProxyPrefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	})
	handler := buildNetworkTrustHandler(t, resolver)

	testCases := []struct {
		name           string
		path           string
		remoteAddress  string
		forwardedFor   string
		expectedStatus int
	}{
		{name: "untrusted MCP peer", path: "/mcp", remoteAddress: "203.0.113.10:443", forwardedFor: "198.51.100.10", expectedStatus: http.StatusForbidden},
		{name: "untrusted panel peer", path: "/panel/v1/auth/login", remoteAddress: "203.0.113.10:443", forwardedFor: "198.51.100.10", expectedStatus: http.StatusForbidden},
		{name: "trusted headerless MCP peer", path: "/mcp", remoteAddress: "192.0.2.10:443", expectedStatus: http.StatusBadRequest},
		{name: "trusted headerless panel peer", path: "/panel/v1/auth/login", remoteAddress: "192.0.2.10:443", expectedStatus: http.StatusBadRequest},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, testCase.path, nil)
			request.RemoteAddr = testCase.remoteAddress
			if testCase.forwardedFor != "" {
				request.Header.Set("X-Forwarded-For", testCase.forwardedFor)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.expectedStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, testCase.expectedStatus, response.Body.String())
			}
		})
	}
}
