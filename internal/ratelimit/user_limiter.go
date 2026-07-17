package ratelimit

import (
	"net/http"
	"sync"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/usage"
	"golang.org/x/time/rate"
)

type entry struct {
	limiter  *rate.Limiter
	rpm      int
	lastSeen time.Time
}

// UserLimiter 按用户 ID 共享 RPM 令牌桶（用户下所有 API Key 共用）。
type UserLimiter struct {
	mu        sync.Mutex
	entries   map[string]*entry
	closeOnce sync.Once
	stop      chan struct{}
}

// NewUserLimiter 创建用户级限流器；实际限流速率来自请求上的 AuthenticatedUser.RPM。
func NewUserLimiter() *UserLimiter {
	limiter := &UserLimiter{
		entries: make(map[string]*entry),
		stop:    make(chan struct{}),
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
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[userID]
	if !ok {
		e = &entry{
			limiter: l.limitFor(rpm),
			rpm:     rpm,
		}
		l.entries[userID] = e
	} else if e.rpm != rpm {
		e.limiter = l.limitFor(rpm)
		e.rpm = rpm
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
