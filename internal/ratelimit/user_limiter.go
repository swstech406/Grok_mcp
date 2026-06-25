package ratelimit

import (
	"net/http"
	"sync"
	"time"

	"github.com/grok-mcp/internal/auth"
	"golang.org/x/time/rate"
)

type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// UserLimiter 按用户 ID 共享 RPM 令牌桶（用户下所有 API Key 共用）。
type UserLimiter struct {
	defaultPerMin int
	mu            sync.Mutex
	entries       map[string]*entry
	closeOnce     sync.Once
	stop          chan struct{}
}

// NewUserLimiter 创建用户级限流器。
func NewUserLimiter(defaultPerMin int) *UserLimiter {
	if defaultPerMin <= 0 {
		defaultPerMin = 60
	}
	l := &UserLimiter{
		defaultPerMin: defaultPerMin,
		entries:       make(map[string]*entry),
		stop:          make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

func (l *UserLimiter) limitFor(rpm int) *rate.Limiter {
	perMin := rpm
	if perMin <= 0 {
		perMin = l.defaultPerMin
	}
	burst := perMin
	if burst < 1 {
		burst = 1
	}
	return rate.NewLimiter(rate.Every(time.Minute/time.Duration(perMin)), burst)
}

func (l *UserLimiter) allow(userID string, rpm int) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[userID]
	if !ok {
		e = &entry{limiter: l.limitFor(rpm)}
		l.entries[userID] = e
	} else {
		want := l.limitFor(rpm)
		if e.limiter.Limit() != want.Limit() || e.limiter.Burst() != want.Burst() {
			e.limiter = want
		}
	}
	e.lastSeen = now
	return e.limiter.Allow()
}

func (l *UserLimiter) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-30 * time.Minute)
			l.mu.Lock()
			for id, e := range l.entries {
				if e.lastSeen.Before(cutoff) {
					delete(l.entries, id)
				}
			}
			l.mu.Unlock()
		}
	}
}

// Close 停止后台清理。
func (l *UserLimiter) Close() {
	l.closeOnce.Do(func() { close(l.stop) })
}

// UserMiddleware 对已鉴权 MCP 请求按用户 RPM 限流。
func (l *UserLimiter) UserMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := auth.UserFromContext(r.Context())
			if !ok {
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