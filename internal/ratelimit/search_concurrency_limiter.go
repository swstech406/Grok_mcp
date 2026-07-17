package ratelimit

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/usage"
)

const (
	// SearchQueueTimeHeader exposes how long the request spent attempting to
	// reserve search concurrency. Acquisition is intentionally non-blocking.
	SearchQueueTimeHeader = "X-Grok-Search-Queue-Time-Ms"

	searchConcurrencyRetryAfterSeconds = 1
	searchConcurrencyCleanupInterval   = 10 * time.Minute
	searchConcurrencyEntryIdleTimeout  = 30 * time.Minute
)

type userConcurrencyEntry struct {
	active   int
	lastSeen time.Time
}

// SearchConcurrencyLimiter bounds concurrent upstream searches globally and
// per authenticated user. It rejects immediately instead of queueing requests
// so saturated capacity cannot accumulate waiting HTTP/SSE goroutines.
type SearchConcurrencyLimiter struct {
	mu           sync.Mutex
	globalLimit  int
	perUserLimit int
	globalActive int
	entries      map[string]*userConcurrencyEntry
	closeOnce    sync.Once
	stop         chan struct{}
}

// NewSearchConcurrencyLimiter creates a limiter with initial capacities.
// UpdateLimits can replace them while active requests retain valid leases.
func NewSearchConcurrencyLimiter(globalLimit, perUserLimit int) *SearchConcurrencyLimiter {
	if globalLimit <= 0 {
		panic("global search concurrency limit must be positive")
	}
	if perUserLimit <= 0 {
		panic("per-user search concurrency limit must be positive")
	}
	if perUserLimit > globalLimit {
		panic("per-user search concurrency limit must not exceed global limit")
	}

	limiter := &SearchConcurrencyLimiter{
		globalLimit:  globalLimit,
		perUserLimit: perUserLimit,
		entries:      make(map[string]*userConcurrencyEntry),
		stop:         make(chan struct{}),
	}
	go limiter.cleanupLoop()
	return limiter
}

// UpdateLimits applies new admission capacities to subsequent requests. Active
// searches retain their leases; after a decrease, new searches are rejected
// until active counts fall below the new limits.
func (limiter *SearchConcurrencyLimiter) UpdateLimits(globalLimit, perUserLimit int) error {
	if globalLimit <= 0 {
		return fmt.Errorf("global search concurrency limit must be positive")
	}
	if perUserLimit <= 0 {
		return fmt.Errorf("per-user search concurrency limit must be positive")
	}
	if perUserLimit > globalLimit {
		return fmt.Errorf("per-user search concurrency limit must not exceed global limit")
	}

	limiter.mu.Lock()
	limiter.globalLimit = globalLimit
	limiter.perUserLimit = perUserLimit
	limiter.mu.Unlock()
	return nil
}

// Limits returns the capacities currently applied to new search requests.
func (limiter *SearchConcurrencyLimiter) Limits() (globalLimit, perUserLimit int) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.globalLimit, limiter.perUserLimit
}

// Middleware limits only tool calls selected by isSearchTool. It must run after
// tool-name extraction and authentication, and before quota and usage tracking.
func (limiter *SearchConcurrencyLimiter) Middleware(isSearchTool func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			toolName, hasToolName := usage.ToolNameFromContext(request.Context())
			if !hasToolName || toolName == "" || isSearchTool == nil || !isSearchTool(toolName) {
				next.ServeHTTP(responseWriter, request)
				return
			}

			authenticatedUser, hasAuthenticatedUser := auth.UserFromContext(request.Context())
			if !hasAuthenticatedUser || authenticatedUser.ID == "" {
				next.ServeHTTP(responseWriter, request)
				return
			}

			acquisitionStartedAt := time.Now()
			release, rejectionMessage := limiter.tryAcquire(authenticatedUser.ID)
			queueTimeMilliseconds := time.Since(acquisitionStartedAt).Milliseconds()
			responseWriter.Header().Set(SearchQueueTimeHeader, strconv.FormatInt(queueTimeMilliseconds, 10))
			if release == nil {
				responseWriter.Header().Set("Retry-After", strconv.Itoa(searchConcurrencyRetryAfterSeconds))
				http.Error(responseWriter, rejectionMessage, http.StatusServiceUnavailable)
				return
			}
			defer release()
			request = request.WithContext(usage.WithSearchPermitRelease(request.Context(), release))

			next.ServeHTTP(responseWriter, request)
		})
	}
}

func (limiter *SearchConcurrencyLimiter) tryAcquire(userID string) (func(), string) {
	now := time.Now()
	limiter.mu.Lock()
	if limiter.globalActive >= limiter.globalLimit {
		limiter.mu.Unlock()
		return nil, "global search concurrency limit reached"
	}

	userEntry, exists := limiter.entries[userID]
	if !exists {
		userEntry = &userConcurrencyEntry{}
		limiter.entries[userID] = userEntry
	}
	userEntry.lastSeen = now
	if userEntry.active >= limiter.perUserLimit {
		limiter.mu.Unlock()
		return nil, "user search concurrency limit reached"
	}
	limiter.globalActive++
	userEntry.active++
	limiter.mu.Unlock()

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			limiter.mu.Lock()
			limiter.globalActive--
			userEntry.active--
			userEntry.lastSeen = time.Now()
			limiter.mu.Unlock()
		})
	}
	return release, ""
}

func (limiter *SearchConcurrencyLimiter) cleanupLoop() {
	ticker := time.NewTicker(searchConcurrencyCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-limiter.stop:
			return
		case now := <-ticker.C:
			limiter.cleanupIdleEntries(now)
		}
	}
}

func (limiter *SearchConcurrencyLimiter) cleanupIdleEntries(now time.Time) {
	cutoff := now.Add(-searchConcurrencyEntryIdleTimeout)
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	for userID, userEntry := range limiter.entries {
		if userEntry.active == 0 && userEntry.lastSeen.Before(cutoff) {
			delete(limiter.entries, userID)
		}
	}
}

// Close stops the background cleanup loop.
func (limiter *SearchConcurrencyLimiter) Close() {
	limiter.closeOnce.Do(func() { close(limiter.stop) })
}
