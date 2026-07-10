package panel

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandlerAuthProtectorSeparatesTrustedProxyClientBuckets(t *testing.T) {
	trustedProxyNetwork := mustParsePanelCIDR(t, "203.0.113.0/24")
	handler := &Handler{TrustedProxies: []*net.IPNet{trustedProxyNetwork}}
	authProtector := handler.authProtector()

	allowedRequestCount := 0
	protectedHandler := authProtector.RateLimitAuthEndpoint(authEndpointRegister, http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		allowedRequestCount++
		responseWriter.WriteHeader(http.StatusOK)
	}))

	performRequest := func(forwardedClientIP string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/register", nil)
		request.RemoteAddr = "203.0.113.10:8443"
		request.Header.Set("X-Forwarded-For", forwardedClientIP)
		responseRecorder := httptest.NewRecorder()
		protectedHandler.ServeHTTP(responseRecorder, request)
		return responseRecorder
	}

	for requestIndex := 0; requestIndex < 10; requestIndex++ {
		responseRecorder := performRequest("198.51.100.10")
		if responseRecorder.Code != http.StatusOK {
			t.Fatalf("client A request %d status = %d, want %d", requestIndex+1, responseRecorder.Code, http.StatusOK)
		}
	}

	clientBResponse := performRequest("198.51.100.11")
	if clientBResponse.Code != http.StatusOK {
		t.Fatalf("client B should have a separate rate-limit bucket, status = %d", clientBResponse.Code)
	}

	limitedClientAResponse := performRequest("198.51.100.10")
	if limitedClientAResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("client A request after exhausting its bucket status = %d, want %d", limitedClientAResponse.Code, http.StatusTooManyRequests)
	}
	if allowedRequestCount != 11 {
		t.Fatalf("allowed request count = %d, want %d", allowedRequestCount, 11)
	}
}

func TestAuthProtectorSeparatesLoginLockoutsByTrustedProxyClientIP(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		TrustedProxies:        []*net.IPNet{mustParsePanelCIDR(t, "203.0.113.0/24")},
		LoginFailureThreshold: 1,
		LoginBaseLockout:      time.Minute,
		LoginMaxLockout:       time.Minute,
	})

	clientARequest := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/login", nil)
	clientARequest.RemoteAddr = "203.0.113.10:8443"
	clientARequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	clientBRequest := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/login", nil)
	clientBRequest.RemoteAddr = "203.0.113.10:8443"
	clientBRequest.Header.Set("X-Forwarded-For", "198.51.100.11")

	clientAIP := authProtector.clientIP(clientARequest)
	clientBIP := authProtector.clientIP(clientBRequest)
	if clientAIP == clientBIP {
		t.Fatalf("trusted proxy clients resolved to the same IP %q", clientAIP)
	}

	authProtector.RecordLoginFailure("alice", clientAIP)
	if locked, _ := authProtector.LoginLocked("alice", clientAIP); !locked {
		t.Fatalf("client A should be locked after reaching the failure threshold")
	}
	if locked, _ := authProtector.LoginLocked("alice", clientBIP); locked {
		t.Fatalf("client B must not inherit client A's login lockout")
	}
}

func mustParsePanelCIDR(t *testing.T, rawCIDR string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(rawCIDR)
	if err != nil {
		t.Fatalf("parse CIDR %q: %v", rawCIDR, err)
	}
	return network
}
