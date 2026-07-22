package ratelimit

import (
	"hash/maphash"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/usage"
	"golang.org/x/time/rate"
)

const (
	defaultUserLimiterMaximumEntries  = 16384
	defaultUserLimiterFallbackBuckets = 64
	defaultUserLimiterEntryIdleTTL    = 30 * time.Minute
	defaultUserLimiterCleanupInterval = 10 * time.Minute
	maximumUserLimiterEntries         = 65536
	maximumUserLimiterFallbackBuckets = 1024
)

type entry struct {
	limiter  *rate.Limiter
	rpm      int
	lastSeen time.Time
}

type userLimiterFallbackBucket struct {
	limiter  *rate.Limiter
	rpm      int
	lastSeen time.Time
}

// UserLimiterConfig controls the bounded per-user RPM registry. Zero values
// use production defaults.
type UserLimiterConfig struct {
	MaximumEntries      int
	FallbackBucketCount int
	EntryIdleTTL        time.Duration
	CleanupInterval     time.Duration
}

// UserLimiterMetricsSnapshot reports aggregate registry state without user IDs.
type UserLimiterMetricsSnapshot struct {
	CurrentEntries        int64  `json:"current_entries"`
	MaximumEntries        int64  `json:"maximum_entries"`
	FallbackBucketCount   int64  `json:"fallback_bucket_count"`
	DedicatedAdmissions   uint64 `json:"dedicated_admissions"`
	ExpiredEntriesRemoved uint64 `json:"expired_entries_removed"`
	FallbackRequests      uint64 `json:"fallback_requests"`
	FallbackRejections    uint64 `json:"fallback_rejections"`
}

// UserLimiter 按用户 ID 共享 RPM 令牌桶（用户下所有 API Key 共用）。
type UserLimiter struct {
	mu                  sync.Mutex
	entries             map[string]*entry
	fallbackBuckets     []userLimiterFallbackBucket
	maximumEntries      int
	entryIdleTTL        time.Duration
	nextExpiryCheck     time.Time
	fallbackHashSeed    maphash.Seed
	currentEntries      atomic.Int64
	dedicatedAdmissions atomic.Uint64
	expiredEntries      atomic.Uint64
	fallbackRequests    atomic.Uint64
	fallbackRejections  atomic.Uint64
	closeOnce           sync.Once
	stop                chan struct{}
	workerDone          chan struct{}
	cleanupInterval     time.Duration
}

// NewUserLimiter 创建用户级限流器；实际限流速率来自请求上的 AuthenticatedUser.RPM。
func NewUserLimiter() *UserLimiter {
	return NewUserLimiterWithConfig(UserLimiterConfig{})
}

// NewUserLimiterWithConfig creates a capacity-bounded per-user limiter.
func NewUserLimiterWithConfig(config UserLimiterConfig) *UserLimiter {
	if config.MaximumEntries <= 0 {
		config.MaximumEntries = defaultUserLimiterMaximumEntries
	}
	if config.MaximumEntries > maximumUserLimiterEntries {
		config.MaximumEntries = maximumUserLimiterEntries
	}
	if config.FallbackBucketCount <= 0 {
		config.FallbackBucketCount = defaultUserLimiterFallbackBuckets
	}
	if config.FallbackBucketCount > maximumUserLimiterFallbackBuckets {
		config.FallbackBucketCount = maximumUserLimiterFallbackBuckets
	}
	if config.EntryIdleTTL <= 0 {
		config.EntryIdleTTL = defaultUserLimiterEntryIdleTTL
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = defaultUserLimiterCleanupInterval
	}

	limiter := &UserLimiter{
		entries:          make(map[string]*entry),
		fallbackBuckets:  make([]userLimiterFallbackBucket, config.FallbackBucketCount),
		maximumEntries:   config.MaximumEntries,
		entryIdleTTL:     config.EntryIdleTTL,
		fallbackHashSeed: maphash.MakeSeed(),
		stop:             make(chan struct{}),
		workerDone:       make(chan struct{}),
		cleanupInterval:  config.CleanupInterval,
	}
	go limiter.cleanupLoop()
	return limiter
}

func (l *UserLimiter) limitFor(rpm int) *rate.Limiter {
	perMin := rpm
	// 防御：middleware 不应在非正 rpm 时调用 allow；若仍落入此处，使用 1 RPM 避免 rate.Every 非法。
	if perMin <= 0 {
		perMin = 1
	}
	burst := perMin
	if burst < 1 {
		burst = 1
	}
	return rate.NewLimiter(rate.Every(time.Minute/time.Duration(perMin)), burst)
}

func (l *UserLimiter) allow(userID string, rpm int) bool {
	return l.allowAt(userID, rpm, time.Now())
}

func (l *UserLimiter) allowAt(userID string, rpm int, now time.Time) bool {
	l.mu.Lock()
	userEntry, exists := l.entries[userID]
	if exists {
		if userEntry.rpm != rpm {
			userEntry.limiter.SetLimitAt(now, rate.Every(time.Minute/time.Duration(rpm)))
			userEntry.limiter.SetBurstAt(now, rpm)
			userEntry.rpm = rpm
		}
		userEntry.lastSeen = now
		l.recordPotentialExpiryLocked(now)
		tokenBucket := userEntry.limiter
		l.mu.Unlock()
		return tokenBucket.AllowN(now, 1)
	}

	shouldCheckForExpiredEntries := l.nextExpiryCheck.IsZero() || !now.Before(l.nextExpiryCheck)
	if len(l.entries) >= l.maximumEntries && shouldCheckForExpiredEntries {
		l.cleanupExpiredEntriesLocked(now)
	}
	if len(l.entries) < l.maximumEntries {
		userEntry = &entry{
			limiter:  l.limitFor(rpm),
			rpm:      rpm,
			lastSeen: now,
		}
		l.entries[userID] = userEntry
		l.recordPotentialExpiryLocked(now)
		l.currentEntries.Add(1)
		l.dedicatedAdmissions.Add(1)
		tokenBucket := userEntry.limiter
		l.mu.Unlock()
		return tokenBucket.AllowN(now, 1)
	}

	fallbackBucketIndex := l.fallbackBucketIndexFor(userID)
	fallbackBucket := &l.fallbackBuckets[fallbackBucketIndex]
	if fallbackBucket.limiter == nil || !fallbackBucket.lastSeen.Add(l.entryIdleTTL).After(now) {
		fallbackBucket.limiter = l.limitFor(rpm)
		fallbackBucket.rpm = rpm
	} else if rpm < fallbackBucket.rpm {
		// A shared bucket may become stricter, but never becomes more permissive
		// while active. This prevents a high-RPM overflow identity from resetting
		// a lower-RPM bucket and obtaining a fresh burst.
		fallbackBucket.limiter.SetLimitAt(now, rate.Every(time.Minute/time.Duration(rpm)))
		fallbackBucket.limiter.SetBurstAt(now, rpm)
		fallbackBucket.rpm = rpm
	}
	fallbackBucket.lastSeen = now
	l.fallbackRequests.Add(1)
	tokenBucket := fallbackBucket.limiter
	l.mu.Unlock()

	allowed := tokenBucket.AllowN(now, 1)
	if !allowed {
		l.fallbackRejections.Add(1)
	}
	return allowed
}

func (l *UserLimiter) fallbackBucketIndexFor(userID string) int {
	return int(maphash.String(l.fallbackHashSeed, userID) % uint64(len(l.fallbackBuckets)))
}

func (l *UserLimiter) cleanupLoop() {
	defer close(l.workerDone)
	ticker := time.NewTicker(l.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case now := <-ticker.C:
			l.mu.Lock()
			l.cleanupExpiredEntriesLocked(now)
			l.mu.Unlock()
		}
	}
}

func (l *UserLimiter) cleanupExpiredEntriesLocked(now time.Time) {
	removedEntryCount := 0
	nextExpiryCheck := time.Time{}
	for userID, userEntry := range l.entries {
		entryExpiry := userEntry.lastSeen.Add(l.entryIdleTTL)
		if !entryExpiry.After(now) {
			delete(l.entries, userID)
			removedEntryCount++
			continue
		}
		if nextExpiryCheck.IsZero() || entryExpiry.Before(nextExpiryCheck) {
			nextExpiryCheck = entryExpiry
		}
	}
	l.nextExpiryCheck = nextExpiryCheck
	if removedEntryCount > 0 {
		l.currentEntries.Add(-int64(removedEntryCount))
		l.expiredEntries.Add(uint64(removedEntryCount))
	}
}

func (l *UserLimiter) recordPotentialExpiryLocked(lastSeen time.Time) {
	entryExpiry := lastSeen.Add(l.entryIdleTTL)
	if l.nextExpiryCheck.IsZero() || entryExpiry.Before(l.nextExpiryCheck) {
		l.nextExpiryCheck = entryExpiry
	}
}

// Metrics returns a lock-free snapshot of bounded registry state.
func (l *UserLimiter) Metrics() UserLimiterMetricsSnapshot {
	if l == nil {
		return UserLimiterMetricsSnapshot{}
	}
	return UserLimiterMetricsSnapshot{
		CurrentEntries:        l.currentEntries.Load(),
		MaximumEntries:        int64(l.maximumEntries),
		FallbackBucketCount:   int64(len(l.fallbackBuckets)),
		DedicatedAdmissions:   l.dedicatedAdmissions.Load(),
		ExpiredEntriesRemoved: l.expiredEntries.Load(),
		FallbackRequests:      l.fallbackRequests.Load(),
		FallbackRejections:    l.fallbackRejections.Load(),
	}
}

// Close 停止后台清理。
func (l *UserLimiter) Close() {
	l.closeOnce.Do(func() {
		close(l.stop)
		<-l.workerDone
	})
}

// UserMiddleware 对已鉴权的 tools/call 按用户 RPM 限流。
// initialize / tools/list / ping 等握手请求不计入 RPM，与 quota/usage 口径一致。
// 需挂在 ExtractToolNameMiddleware 之后，以便从 context 读取工具名；
// 未提取到工具名时不限流（非 tools/call 或尚未解析）。
// rpm==0 表示不限；负数为非法配置。
func (l *UserLimiter) UserMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			toolName, hasToolName := usage.ToolNameFromContext(r.Context())
			if !hasToolName || toolName == "" {
				next.ServeHTTP(w, r)
				return
			}
			user, ok := auth.UserFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if user.RPM < 0 {
				http.Error(w, "invalid rate limit", http.StatusInternalServerError)
				return
			}
			if user.RPM == 0 {
				next.ServeHTTP(w, r)
				return
			}
			if !l.allow(user.ID, user.RPM) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
