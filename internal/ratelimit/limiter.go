// Package ratelimit 为每个 API Key 维护内存中的令牌桶限流（requests per minute）。
package ratelimit

import (
	"net/http"
	"sync"
	"time"

	"github.com/grok-mcp/internal/auth"
	"golang.org/x/time/rate"
)

// Limiter 按 key ID 缓存 rate.Limiter；密钥的 RateLimit 为 0 时使用 defaultPerMin。
type Limiter struct {
	defaultPerMin int
	mu            sync.Mutex
	entries       map[string]*entry
	closeOnce     sync.Once
	stop          chan struct{}
}

type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// New 创建限流器并启动后台协程，定期剔除 30 分钟未活跃的 limiter 条目以控制内存。
func New(defaultPerMin int) *Limiter {
	if defaultPerMin <= 0 {
		defaultPerMin = 60
	}
	l := &Limiter{
		defaultPerMin: defaultPerMin,
		entries:       make(map[string]*entry),
		stop:          make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

func (l *Limiter) limitForKey(keyID string, keyLimit int) *rate.Limiter {
	perMin := keyLimit
	if perMin <= 0 {
		perMin = l.defaultPerMin
	}
	burst := perMin
	if burst < 1 {
		burst = 1
	}
	return rate.NewLimiter(rate.Every(time.Minute/time.Duration(perMin)), burst)
}

func (l *Limiter) allow(keyID string, keyLimit int) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[keyID]
	if !ok {
		e = &entry{limiter: l.limitForKey(keyID, keyLimit)}
		l.entries[keyID] = e
	} else if keyLimit > 0 && int(e.limiter.Limit()*60) != keyLimit {
		// 管理端修改 rate_limit 后，下次请求会重建 limiter。
		e.limiter = l.limitForKey(keyID, keyLimit)
	}
	e.lastSeen = now
	return e.limiter.Allow()
}

func (l *Limiter) cleanupLoop() {
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

// Close 停止后台清理协程；幂等，供 runHTTP 在退出时 defer 调用。
func (l *Limiter) Close() {
	l.closeOnce.Do(func() { close(l.stop) })
}

// Middleware 对已通过 APIKey 鉴权的请求执行 Allow()；超限返回 429 与 Retry-After: 60。
func (l *Limiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := auth.APIKeyFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if !l.allow(key.ID, key.RateLimit) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
