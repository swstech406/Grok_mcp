package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPLimiter 按来源 IP 共享令牌桶，适合放在鉴权之前保护认证存储。
// 默认仅使用 RemoteAddr，避免公网直连时伪造 X-Forwarded-For。
// 当配置了可信反代 CIDR 且 RemoteAddr 落在其中时，才解析 X-Real-IP / X-Forwarded-For。
type IPLimiter struct {
	requestsPerMinute int
	clientIPResolver  *ClientIPResolver
	mu                sync.Mutex
	entries           map[string]*ipEntry
	closeOnce         sync.Once
	stop              chan struct{}
}

// NewIPLimiter 创建来源 IP 限流器。
func NewIPLimiter(requestsPerMinute int) *IPLimiter {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 60
	}
	limiter := &IPLimiter{
		requestsPerMinute: requestsPerMinute,
		clientIPResolver:  NewClientIPResolver(nil),
		entries:           make(map[string]*ipEntry),
		stop:              make(chan struct{}),
	}
	go limiter.cleanupLoop()
	return limiter
}

// SetTrustedProxies 设置可信反向代理网段；nil/空表示永不信任转发头。
func (limiter *IPLimiter) SetTrustedProxies(networks []*net.IPNet) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.clientIPResolver = NewClientIPResolver(networks)
}

func (limiter *IPLimiter) allow(clientAddress string) bool {
	now := time.Now()
	clientAddress = strings.TrimSpace(clientAddress)
	if clientAddress == "" {
		clientAddress = "unknown"
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	entry, ok := limiter.entries[clientAddress]
	if !ok {
		entry = &ipEntry{limiter: limiter.newTokenBucket()}
		limiter.entries[clientAddress] = entry
	}
	entry.lastSeen = now
	return entry.limiter.Allow()
}

func (limiter *IPLimiter) newTokenBucket() *rate.Limiter {
	return rate.NewLimiter(rate.Every(time.Minute/time.Duration(limiter.requestsPerMinute)), limiter.requestsPerMinute)
}

func (limiter *IPLimiter) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-limiter.stop:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-30 * time.Minute)
			limiter.mu.Lock()
			for clientAddress, entry := range limiter.entries {
				if entry.lastSeen.Before(cutoff) {
					delete(limiter.entries, clientAddress)
				}
			}
			limiter.mu.Unlock()
		}
	}
}

// Close 停止后台清理。
func (limiter *IPLimiter) Close() {
	limiter.closeOnce.Do(func() { close(limiter.stop) })
}

// Middleware 对请求按来源 IP 限流。
// 未配置可信代理时只使用 RemoteAddr；配置后仅当直连对端在可信网段内才解析转发头。
func (limiter *IPLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.allow(limiter.clientIP(r)) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (limiter *IPLimiter) clientIP(r *http.Request) string {
	limiter.mu.Lock()
	resolver := limiter.clientIPResolver
	limiter.mu.Unlock()
	return resolver.Resolve(r)
}
