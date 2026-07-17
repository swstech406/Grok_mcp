package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/usage"
)

func TestSearchConcurrencyMiddlewareBypassesNonSearchTraffic(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(1, 1)
	defer limiter.Close()

	var handlerCalls atomic.Int32
	handler := limiter.Middleware(isTestSearchTool)(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		handlerCalls.Add(1)
		responseWriter.WriteHeader(http.StatusNoContent)
	}))

	for _, toolName := range []string{"", "grok_list_models", "unknown_tool"} {
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, newSearchConcurrencyRequest("user-1", toolName))
		if responseRecorder.Code != http.StatusNoContent {
			t.Fatalf("tool %q status = %d, want %d", toolName, responseRecorder.Code, http.StatusNoContent)
		}
		if responseRecorder.Header().Get(SearchQueueTimeHeader) != "" {
			t.Fatalf("tool %q unexpectedly received queue-time header", toolName)
		}
	}

	if handlerCalls.Load() != 3 {
		t.Fatalf("handler calls = %d, want 3", handlerCalls.Load())
	}
}

func TestSearchConcurrencyMiddlewareRejectsAtGlobalCapacity(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(1, 1)
	defer limiter.Close()

	firstRequestStarted := make(chan struct{})
	releaseFirstRequest := make(chan struct{})
	handler := limiter.Middleware(isTestSearchTool)(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		close(firstRequestStarted)
		<-releaseFirstRequest
		responseWriter.WriteHeader(http.StatusNoContent)
	}))

	firstRequestDone := make(chan struct{})
	go func() {
		defer close(firstRequestDone)
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, newSearchConcurrencyRequest("user-1", "grok_web_search"))
	}()
	waitForTestSignal(t, firstRequestStarted, "first search request to start")

	secondResponseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondResponseRecorder, newSearchConcurrencyRequest("user-2", "grok_x_search"))
	if secondResponseRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", secondResponseRecorder.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(secondResponseRecorder.Body.String(), "global search concurrency limit reached") {
		t.Fatalf("unexpected response body: %q", secondResponseRecorder.Body.String())
	}
	assertConcurrencyRejectionHeaders(t, secondResponseRecorder)

	close(releaseFirstRequest)
	waitForTestSignal(t, firstRequestDone, "first search request to finish")
}

func TestSearchConcurrencyMiddlewareRejectsAtPerUserCapacity(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(2, 1)
	defer limiter.Close()

	firstRequestStarted := make(chan struct{})
	releaseFirstRequest := make(chan struct{})
	handler := limiter.Middleware(isTestSearchTool)(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		close(firstRequestStarted)
		<-releaseFirstRequest
		responseWriter.WriteHeader(http.StatusNoContent)
	}))

	firstRequestDone := make(chan struct{})
	go func() {
		defer close(firstRequestDone)
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, newSearchConcurrencyRequest("user-1", "grok_web_search"))
	}()
	waitForTestSignal(t, firstRequestStarted, "first user search request to start")

	secondResponseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondResponseRecorder, newSearchConcurrencyRequest("user-1", "grok_x_search"))
	if secondResponseRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", secondResponseRecorder.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(secondResponseRecorder.Body.String(), "user search concurrency limit reached") {
		t.Fatalf("unexpected response body: %q", secondResponseRecorder.Body.String())
	}
	assertConcurrencyRejectionHeaders(t, secondResponseRecorder)

	close(releaseFirstRequest)
	waitForTestSignal(t, firstRequestDone, "first user search request to finish")
}

func TestSearchConcurrencyMiddlewareAllowsDifferentUsersWithinGlobalCapacity(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(2, 1)
	defer limiter.Close()

	firstRequestStarted := make(chan struct{})
	releaseFirstRequest := make(chan struct{})
	handler := limiter.Middleware(isTestSearchTool)(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		authenticatedUser, _ := auth.UserFromContext(request.Context())
		if authenticatedUser.ID == "user-1" {
			close(firstRequestStarted)
			<-releaseFirstRequest
		}
		responseWriter.WriteHeader(http.StatusNoContent)
	}))

	firstRequestDone := make(chan struct{})
	go func() {
		defer close(firstRequestDone)
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, newSearchConcurrencyRequest("user-1", "grok_web_search"))
	}()
	waitForTestSignal(t, firstRequestStarted, "first user search request to start")

	secondResponseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondResponseRecorder, newSearchConcurrencyRequest("user-2", "grok_x_search"))
	if secondResponseRecorder.Code != http.StatusNoContent {
		t.Fatalf("different user status = %d, want %d", secondResponseRecorder.Code, http.StatusNoContent)
	}
	assertQueueTimeHeader(t, secondResponseRecorder)

	close(releaseFirstRequest)
	waitForTestSignal(t, firstRequestDone, "first user search request to finish")
}

func TestSearchConcurrencyMiddlewareReleasesLeaseAfterHandlerReturns(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(1, 1)
	defer limiter.Close()

	handler := limiter.Middleware(isTestSearchTool)(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusNoContent)
	}))

	for requestIndex := 0; requestIndex < 2; requestIndex++ {
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, newSearchConcurrencyRequest("user-1", "grok_web_search"))
		if responseRecorder.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want %d", requestIndex, responseRecorder.Code, http.StatusNoContent)
		}
		assertQueueTimeHeader(t, responseRecorder)
	}
}

func TestSearchConcurrencyMiddlewareReleasesLeaseAfterPanic(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(1, 1)
	defer limiter.Close()

	var handlerCalls atomic.Int32
	handler := limiter.Middleware(isTestSearchTool)(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		if handlerCalls.Add(1) == 1 {
			panic("test panic")
		}
		responseWriter.WriteHeader(http.StatusNoContent)
	}))

	func() {
		defer func() {
			if recoveredValue := recover(); recoveredValue == nil {
				t.Fatalf("expected first request to panic")
			}
		}()
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, newSearchConcurrencyRequest("user-1", "grok_web_search"))
	}()

	secondResponseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondResponseRecorder, newSearchConcurrencyRequest("user-1", "grok_web_search"))
	if secondResponseRecorder.Code != http.StatusNoContent {
		t.Fatalf("status after panic = %d, want %d", secondResponseRecorder.Code, http.StatusNoContent)
	}
}

func TestSearchConcurrencyCleanupPreservesActiveEntries(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(1, 1)
	defer limiter.Close()

	release, rejectionMessage := limiter.tryAcquire("user-1")
	if release == nil {
		t.Fatalf("unexpected acquisition rejection: %s", rejectionMessage)
	}

	oldTimestamp := time.Now().Add(-2 * searchConcurrencyEntryIdleTimeout)
	limiter.mu.Lock()
	limiter.entries["user-1"].lastSeen = oldTimestamp
	limiter.mu.Unlock()
	limiter.cleanupIdleEntries(time.Now())

	limiter.mu.Lock()
	_, activeEntryExists := limiter.entries["user-1"]
	limiter.mu.Unlock()
	if !activeEntryExists {
		t.Fatalf("cleanup removed an active user entry")
	}

	release()
	limiter.cleanupIdleEntries(time.Now().Add(searchConcurrencyEntryIdleTimeout + time.Second))
	limiter.mu.Lock()
	_, releasedEntryExists := limiter.entries["user-1"]
	limiter.mu.Unlock()
	if releasedEntryExists {
		t.Fatalf("cleanup retained an idle released user entry")
	}
}

func TestSearchConcurrencyLimiterIncreasingLimitsAdmitsNewRequests(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(1, 1)
	defer limiter.Close()

	firstRelease, rejectionMessage := limiter.tryAcquire("user-1")
	if firstRelease == nil {
		t.Fatalf("first acquisition rejected: %s", rejectionMessage)
	}
	defer firstRelease()

	if secondRelease, _ := limiter.tryAcquire("user-1"); secondRelease != nil {
		secondRelease()
		t.Fatalf("second acquisition unexpectedly succeeded before limit increase")
	}
	if err := limiter.UpdateLimits(2, 2); err != nil {
		t.Fatalf("UpdateLimits failed: %v", err)
	}

	secondRelease, rejectionMessage := limiter.tryAcquire("user-1")
	if secondRelease == nil {
		t.Fatalf("second acquisition rejected after limit increase: %s", rejectionMessage)
	}
	secondRelease()
}

func TestSearchConcurrencyLimiterDecreasingLimitsPreservesActiveLeases(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(2, 2)
	defer limiter.Close()

	firstRelease, firstRejection := limiter.tryAcquire("user-1")
	secondRelease, secondRejection := limiter.tryAcquire("user-2")
	if firstRelease == nil || secondRelease == nil {
		t.Fatalf("initial acquisitions rejected: first=%q second=%q", firstRejection, secondRejection)
	}

	if err := limiter.UpdateLimits(1, 1); err != nil {
		t.Fatalf("UpdateLimits failed: %v", err)
	}
	if release, rejectionMessage := limiter.tryAcquire("user-3"); release != nil {
		release()
		t.Fatalf("acquisition unexpectedly succeeded above decreased global limit")
	} else if rejectionMessage != "global search concurrency limit reached" {
		t.Fatalf("unexpected rejection: %q", rejectionMessage)
	}

	firstRelease()
	if release, _ := limiter.tryAcquire("user-3"); release != nil {
		release()
		t.Fatalf("acquisition unexpectedly succeeded while active count equaled decreased limit")
	}

	secondRelease()
	thirdRelease, rejectionMessage := limiter.tryAcquire("user-3")
	if thirdRelease == nil {
		t.Fatalf("acquisition rejected after old leases released: %s", rejectionMessage)
	}
	thirdRelease()
}

func TestSearchConcurrencyLimiterRejectsInvalidUpdatesWithoutChangingLimits(t *testing.T) {
	limiter := NewSearchConcurrencyLimiter(2, 1)
	defer limiter.Close()

	for _, limits := range [][2]int{{0, 1}, {2, 0}, {1, 2}} {
		if err := limiter.UpdateLimits(limits[0], limits[1]); err == nil {
			t.Fatalf("UpdateLimits(%d, %d) unexpectedly succeeded", limits[0], limits[1])
		}
	}

	firstRelease, firstRejection := limiter.tryAcquire("user-1")
	secondRelease, secondRejection := limiter.tryAcquire("user-2")
	if firstRelease == nil || secondRelease == nil {
		t.Fatalf("valid original limits changed: first=%q second=%q", firstRejection, secondRejection)
	}
	firstRelease()
	secondRelease()
}

func newSearchConcurrencyRequest(userID, toolName string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	authenticatedUser := &auth.AuthenticatedUser{User: store.User{ID: userID}}
	requestContext := auth.WithUser(request.Context(), authenticatedUser)
	requestContext = usage.WithToolName(requestContext, toolName)
	return request.WithContext(requestContext)
}

func isTestSearchTool(toolName string) bool {
	return toolName == "grok_web_search" || toolName == "grok_x_search"
}

func assertConcurrencyRejectionHeaders(t *testing.T, responseRecorder *httptest.ResponseRecorder) {
	t.Helper()
	if responseRecorder.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", responseRecorder.Header().Get("Retry-After"))
	}
	assertQueueTimeHeader(t, responseRecorder)
}

func assertQueueTimeHeader(t *testing.T, responseRecorder *httptest.ResponseRecorder) {
	t.Helper()
	rawQueueTime := responseRecorder.Header().Get(SearchQueueTimeHeader)
	queueTimeMilliseconds, err := strconv.ParseInt(rawQueueTime, 10, 64)
	if err != nil || queueTimeMilliseconds < 0 {
		t.Fatalf("invalid %s value %q", SearchQueueTimeHeader, rawQueueTime)
	}
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
