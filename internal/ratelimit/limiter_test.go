package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
)

func TestRateLimitMiddleware(t *testing.T) {
	lim := New(2)
	defer lim.Close()
	key := &store.APIKey{ID: "k1", RateLimit: 2}

	h := lim.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(auth.WithAPIKey(req.Context(), key))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithAPIKey(req.Context(), key))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}
