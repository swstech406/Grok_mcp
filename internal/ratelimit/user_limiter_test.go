package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/usage"
	"golang.org/x/time/rate"
)

func TestUserMiddlewareRejectsNegativeRPM(t *testing.T) {
	l := NewUserLimiter()
	defer l.Close()

	var called bool
	h := l.UserMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	user := &auth.AuthenticatedUser{User: store.User{ID: "u1"}, RPM: -1}
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	ctx := auth.WithUser(req.Context(), user)
	ctx = usage.WithToolName(ctx, "grok_web_search")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if called {
		t.Fatal("next handler must not run for negative RPM")
	}
}

func TestUserMiddlewareSkipsNonToolCallTraffic(t *testing.T) {
	l := NewUserLimiter()
	defer l.Close()

	var called int
	h := l.UserMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))

	user := &auth.AuthenticatedUser{User: store.User{ID: "u1"}, RPM: 1}
	for requestIndex := 0; requestIndex < 3; requestIndex++ {
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		ctx := auth.WithUser(req.Context(), user)
		ctx = usage.WithToolName(ctx, "")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("non tools/call request %d status = %d, want %d", requestIndex, rec.Code, http.StatusOK)
		}
	}
	if called != 3 {
		t.Fatalf("non tools/call traffic should pass through without RPM limiting, called=%d", called)
	}
}

func TestUserLimiterRebuildsWhenRPMChanges(t *testing.T) {
	l := NewUserLimiter()
	defer l.Close()

	custom := 120
	if !l.allow("u1", custom) {
		t.Fatal("expected allow under custom rpm")
	}
	l.mu.Lock()
	entry := l.entries["u1"]
	customLimit := entry.limiter.Limit()
	l.mu.Unlock()
	if int(customLimit*60) != custom {
		t.Fatalf("custom limit want %d got %v", custom, customLimit)
	}

	updatedRPM := 60
	if !l.allow("u1", updatedRPM) {
		t.Fatal("expected allow under updated rpm")
	}
	l.mu.Lock()
	entry = l.entries["u1"]
	updatedLimit := entry.limiter.Limit()
	l.mu.Unlock()
	want := rate.Every(time.Minute / 60)
	if entry.limiter.Limit() != want || updatedLimit != want {
		t.Fatalf("expected updated limiter %v got %v", want, entry.limiter.Limit())
	}
}
