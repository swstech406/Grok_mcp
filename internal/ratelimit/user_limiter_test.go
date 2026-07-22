package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/usage"
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

func TestUserLimiterBoundsDedicatedEntriesAndUsesFallback(t *testing.T) {
	limiter := NewUserLimiterWithConfig(UserLimiterConfig{
		MaximumEntries:      1,
		FallbackBucketCount: 1,
		CleanupInterval:     time.Hour,
	})
	defer limiter.Close()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)

	if !limiter.allowAt("dedicated-user", 1, now) {
		t.Fatal("first dedicated request was rejected")
	}
	if !limiter.allowAt("fallback-user-one", 1, now) {
		t.Fatal("first fallback request was rejected")
	}
	if limiter.allowAt("fallback-user-two", 1, now) {
		t.Fatal("second identity sharing the fallback bucket should be rejected")
	}
	if limiter.allowAt("dedicated-user", 1, now) {
		t.Fatal("capacity pressure reset the dedicated user's active state")
	}

	metrics := limiter.Metrics()
	if metrics.CurrentEntries != 1 || metrics.MaximumEntries != 1 || metrics.FallbackBucketCount != 1 {
		t.Fatalf("user limiter capacity metrics = %+v", metrics)
	}
	if metrics.FallbackRequests != 2 || metrics.FallbackRejections != 1 {
		t.Fatalf("user limiter fallback metrics = %+v", metrics)
	}
}

func TestUserLimiterReclaimsExpiredDedicatedEntryBeforeFallback(t *testing.T) {
	limiter := NewUserLimiterWithConfig(UserLimiterConfig{
		MaximumEntries:      1,
		FallbackBucketCount: 1,
		EntryIdleTTL:        time.Minute,
		CleanupInterval:     time.Hour,
	})
	defer limiter.Close()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)

	if !limiter.allowAt("expired-user", 1, now) {
		t.Fatal("first dedicated request was rejected")
	}
	if !limiter.allowAt("replacement-user", 1, now.Add(time.Minute)) {
		t.Fatal("replacement user was not admitted after expiry")
	}
	metrics := limiter.Metrics()
	if metrics.CurrentEntries != 1 || metrics.ExpiredEntriesRemoved != 1 || metrics.FallbackRequests != 0 {
		t.Fatalf("user limiter expiry metrics = %+v", metrics)
	}
}

func TestUserLimiterRepeatedOverflowRemainsCapacityBounded(t *testing.T) {
	limiter := NewUserLimiterWithConfig(UserLimiterConfig{
		MaximumEntries:      2,
		FallbackBucketCount: 2,
		EntryIdleTTL:        time.Hour,
		CleanupInterval:     time.Hour,
	})
	defer limiter.Close()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	limiter.allowAt("dedicated-one", 10, now)
	limiter.allowAt("dedicated-two", 10, now)

	for overflowIndex := 0; overflowIndex < 1000; overflowIndex++ {
		limiter.allowAt("overflow-"+strconv.Itoa(overflowIndex), 10, now)
	}
	if entryCount := len(limiter.entries); entryCount != 2 {
		t.Fatalf("dedicated entry count = %d, want 2", entryCount)
	}
	if fallbackRequests := limiter.Metrics().FallbackRequests; fallbackRequests != 1000 {
		t.Fatalf("fallback requests = %d, want 1000", fallbackRequests)
	}
}

func TestUserLimiterFallbackSelectionIsDeterministicWithinProcess(t *testing.T) {
	limiter := NewUserLimiterWithConfig(UserLimiterConfig{MaximumEntries: 1, FallbackBucketCount: 7})
	defer limiter.Close()
	const userID = "deterministic-overflow-user"
	firstIndex := limiter.fallbackBucketIndexFor(userID)
	for selectionIndex := 0; selectionIndex < 100; selectionIndex++ {
		selectedIndex := limiter.fallbackBucketIndexFor(userID)
		if selectedIndex != firstIndex {
			t.Fatalf("fallback index changed from %d to %d", firstIndex, selectedIndex)
		}
	}
}

func TestUserLimiterConcurrentAdmissionNeverExceedsCapacity(t *testing.T) {
	const maximumEntries = 8
	limiter := NewUserLimiterWithConfig(UserLimiterConfig{
		MaximumEntries:      maximumEntries,
		FallbackBucketCount: 4,
		CleanupInterval:     time.Hour,
	})
	defer limiter.Close()
	now := time.Now()
	var waitGroup sync.WaitGroup
	for userIndex := 0; userIndex < 256; userIndex++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			limiter.allowAt("concurrent-user-"+strconv.Itoa(index), 10, now)
		}(userIndex)
	}
	waitGroup.Wait()
	if entryCount := len(limiter.entries); entryCount != maximumEntries {
		t.Fatalf("dedicated entry count = %d, want %d", entryCount, maximumEntries)
	}
	if currentEntries := limiter.Metrics().CurrentEntries; currentEntries != maximumEntries {
		t.Fatalf("current entry metric = %d, want %d", currentEntries, maximumEntries)
	}
}

func TestUserMiddlewareRPMZeroBypassesRegistry(t *testing.T) {
	limiter := NewUserLimiterWithConfig(UserLimiterConfig{MaximumEntries: 1, FallbackBucketCount: 1})
	defer limiter.Close()
	handler := limiter.UserMiddleware()(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	requestContext := auth.WithUser(request.Context(), &auth.AuthenticatedUser{User: store.User{ID: "unlimited-user"}, RPM: 0})
	requestContext = usage.WithToolName(requestContext, "grok_web_search")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request.WithContext(requestContext))

	if response.Code != http.StatusOK {
		t.Fatalf("rpm=0 status = %d, want %d", response.Code, http.StatusOK)
	}
	if metrics := limiter.Metrics(); metrics.CurrentEntries != 0 || metrics.FallbackRequests != 0 {
		t.Fatalf("rpm=0 allocated limiter state: %+v", metrics)
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
