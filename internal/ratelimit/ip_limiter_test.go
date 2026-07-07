package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPLimiterLimitsRequestsByRemoteHost(t *testing.T) {
	limiter := NewIPLimiter(1)
	defer limiter.Close()

	allowedRequests := 0
	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		allowedRequests++
		w.WriteHeader(http.StatusOK)
	}))

	firstRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	firstRequest.RemoteAddr = "198.51.100.10:10001"
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, firstRequest)
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	secondRequest.RemoteAddr = "198.51.100.10:10002"
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, secondRequest)
	if secondRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("second same-host request status = %d, want %d", secondRecorder.Code, http.StatusTooManyRequests)
	}
	if secondRecorder.Header().Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header for rate limited request")
	}

	thirdRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	thirdRequest.RemoteAddr = "198.51.100.11:10003"
	thirdRecorder := httptest.NewRecorder()
	handler.ServeHTTP(thirdRecorder, thirdRequest)
	if thirdRecorder.Code != http.StatusOK {
		t.Fatalf("different-host request status = %d, want %d", thirdRecorder.Code, http.StatusOK)
	}
	if allowedRequests != 2 {
		t.Fatalf("allowed request count = %d, want %d", allowedRequests, 2)
	}
}
