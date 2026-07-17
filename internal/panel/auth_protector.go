package panel

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"golang.org/x/time/rate"
)

const (
	authEndpointLogin    authEndpoint = "login"
	authEndpointRegister authEndpoint = "register"

	minPanelUsernameBytes = 1
	maxPanelUsernameBytes = 128
	minPanelPasswordBytes = 8
	// bcrypt only accepts passwords up to 72 bytes. Rejecting longer inputs before
	// hashing avoids spending CPU on requests that cannot produce a valid login.
	maxPanelPasswordBytes = 72
)

var errInvalidPanelAuthCredentials = errors.New("username must be 1-128 bytes and password must be 8-72 bytes")

type authEndpoint string

type authEndpointLimit struct {
	requestsPerMinute int
	burst             int
}

type authRateLimitEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type loginFailureEntry struct {
	failureCount int
	lastFailure  time.Time
	lockedUntil  time.Time
}

// AuthProtectorConfig controls the in-memory protections for unauthenticated
// panel auth endpoints. Zero values are replaced with conservative defaults.
type AuthProtectorConfig struct {
	LoginIPRequestsPerMinute    int
	LoginIPBurst                int
	RegisterIPRequestsPerMinute int
	RegisterIPBurst             int
	LoginFailureThreshold       int
	LoginFailureWindow          time.Duration
	LoginBaseLockout            time.Duration
	LoginMaxLockout             time.Duration
}

// AuthProtector adds cheap request throttling and short-term login-failure
// lockouts ahead of expensive bcrypt work.
type AuthProtector struct {
	mu sync.Mutex

	now              func() time.Time
	clientIPResolver *ratelimit.ClientIPResolver

	endpointLimits map[authEndpoint]authEndpointLimit
	limiters       map[string]*authRateLimitEntry
	failures       map[string]*loginFailureEntry

	loginFailureThreshold int
	loginFailureWindow    time.Duration
	loginBaseLockout      time.Duration
	loginMaxLockout       time.Duration

	lastCleanup time.Time
}

// NewAuthProtector creates an in-memory auth endpoint protector.
func NewAuthProtector(config AuthProtectorConfig) *AuthProtector {
	if config.LoginIPRequestsPerMinute <= 0 {
		config.LoginIPRequestsPerMinute = 30
	}
	if config.LoginIPBurst <= 0 {
		config.LoginIPBurst = config.LoginIPRequestsPerMinute
	}
	if config.RegisterIPRequestsPerMinute <= 0 {
		config.RegisterIPRequestsPerMinute = 10
	}
	if config.RegisterIPBurst <= 0 {
		config.RegisterIPBurst = config.RegisterIPRequestsPerMinute
	}
	if config.LoginFailureThreshold <= 0 {
		config.LoginFailureThreshold = 5
	}
	if config.LoginFailureWindow <= 0 {
		config.LoginFailureWindow = 15 * time.Minute
	}
	if config.LoginBaseLockout <= 0 {
		config.LoginBaseLockout = time.Minute
	}
	if config.LoginMaxLockout <= 0 {
		config.LoginMaxLockout = 15 * time.Minute
	}
	if config.LoginMaxLockout < config.LoginBaseLockout {
		config.LoginMaxLockout = config.LoginBaseLockout
	}

	now := time.Now
	return &AuthProtector{
		now:              now,
		clientIPResolver: ratelimit.NewClientIPResolver(),
		endpointLimits: map[authEndpoint]authEndpointLimit{
			authEndpointLogin: {
				requestsPerMinute: config.LoginIPRequestsPerMinute,
				burst:             config.LoginIPBurst,
			},
			authEndpointRegister: {
				requestsPerMinute: config.RegisterIPRequestsPerMinute,
				burst:             config.RegisterIPBurst,
			},
		},
		limiters:              make(map[string]*authRateLimitEntry),
		failures:              make(map[string]*loginFailureEntry),
		loginFailureThreshold: config.LoginFailureThreshold,
		loginFailureWindow:    config.LoginFailureWindow,
		loginBaseLockout:      config.LoginBaseLockout,
		loginMaxLockout:       config.LoginMaxLockout,
		lastCleanup:           now(),
	}
}

func (h *Handler) authProtector() *AuthProtector {
	if h.AuthProtector == nil {
		h.AuthProtector = NewAuthProtector(AuthProtectorConfig{})
	}
	return h.AuthProtector
}

// RateLimitAuthEndpoint applies an IP-scoped token bucket to login/register
// only when a valid forwarded client IP can be resolved.
func (p *AuthProtector) RateLimitAuthEndpoint(endpoint authEndpoint, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP, shouldApplyIPProtection, err := p.clientIPForProtection(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, ratelimit.ErrInvalidForwardedClientIPHeaders.Error())
			return
		}
		if !shouldApplyIPProtection {
			next.ServeHTTP(w, r)
			return
		}

		allowed, retryAfter := p.allowAuthRequest(endpoint, clientIP)
		if !allowed {
			writeRetryAfter(w, retryAfter)
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (p *AuthProtector) clientIP(request *http.Request) string {
	return p.clientIPResolver.Resolve(request)
}

func (p *AuthProtector) clientIPForProtection(request *http.Request) (string, bool, error) {
	clientAddress, err := p.clientIPResolver.ResolveAddress(request)
	if err != nil {
		return "", false, err
	}
	if !clientAddress.IsValid() {
		return "", false, nil
	}
	return clientAddress.String(), true, nil
}

func (p *AuthProtector) allowAuthRequest(endpoint authEndpoint, clientIP string) (bool, time.Duration) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupLocked(now)

	limitConfig, ok := p.endpointLimits[endpoint]
	if !ok || limitConfig.requestsPerMinute <= 0 {
		return true, 0
	}
	limiterKey := string(endpoint) + "\x00" + clientIP
	entry, ok := p.limiters[limiterKey]
	if !ok {
		entry = &authRateLimitEntry{limiter: newAuthRateLimiter(limitConfig)}
		p.limiters[limiterKey] = entry
	}
	entry.lastSeen = now

	reservation := entry.limiter.ReserveN(now, 1)
	if !reservation.OK() {
		return false, time.Minute
	}
	delay := reservation.DelayFrom(now)
	if delay > 0 {
		reservation.CancelAt(now)
		return false, delay
	}
	return true, 0
}

func newAuthRateLimiter(limitConfig authEndpointLimit) *rate.Limiter {
	burst := limitConfig.burst
	if burst < 1 {
		burst = 1
	}
	return rate.NewLimiter(rate.Every(time.Minute/time.Duration(limitConfig.requestsPerMinute)), burst)
}

// LoginLocked reports whether recent failures for the username/IP pair require
// a short-term lockout. The pair scope avoids a global username lock that could
// be abused to deny service to a legitimate user.
func (p *AuthProtector) LoginLocked(username, clientIP string) (bool, time.Duration) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupLocked(now)

	failureKey := loginFailureKey(username, clientIP)
	entry := p.failures[failureKey]
	if entry == nil || !entry.lockedUntil.After(now) {
		return false, 0
	}
	return true, entry.lockedUntil.Sub(now)
}

// RecordLoginFailure increments the username/IP failure bucket and starts or
// extends a short lockout after the configured threshold is reached.
func (p *AuthProtector) RecordLoginFailure(username, clientIP string) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupLocked(now)

	failureKey := loginFailureKey(username, clientIP)
	entry := p.failures[failureKey]
	if entry == nil || now.Sub(entry.lastFailure) > p.loginFailureWindow {
		entry = &loginFailureEntry{}
		p.failures[failureKey] = entry
	}
	entry.failureCount++
	entry.lastFailure = now
	if entry.failureCount >= p.loginFailureThreshold {
		entry.lockedUntil = now.Add(p.lockoutDurationForFailures(entry.failureCount))
	}
}

// RecordLoginSuccess clears any failure state for the username/IP pair.
func (p *AuthProtector) RecordLoginSuccess(username, clientIP string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.failures, loginFailureKey(username, clientIP))
}

func (p *AuthProtector) lockoutDurationForFailures(failureCount int) time.Duration {
	excessFailures := failureCount - p.loginFailureThreshold
	if excessFailures < 0 {
		excessFailures = 0
	}
	if excessFailures > 8 {
		excessFailures = 8
	}
	duration := p.loginBaseLockout * time.Duration(1<<excessFailures)
	if duration > p.loginMaxLockout {
		return p.loginMaxLockout
	}
	return duration
}

func (p *AuthProtector) cleanupLocked(now time.Time) {
	const cleanupInterval = 5 * time.Minute
	if now.Sub(p.lastCleanup) < cleanupInterval {
		return
	}
	p.lastCleanup = now

	limiterCutoff := now.Add(-30 * time.Minute)
	for key, entry := range p.limiters {
		if entry.lastSeen.Before(limiterCutoff) {
			delete(p.limiters, key)
		}
	}
	failureCutoff := now.Add(-p.loginFailureWindow)
	for key, entry := range p.failures {
		if entry.lastFailure.Before(failureCutoff) && !entry.lockedUntil.After(now) {
			delete(p.failures, key)
		}
	}
}

func validatePanelAuthCredentials(rawUsername, password string) (string, error) {
	username := strings.TrimSpace(rawUsername)
	if len(username) < minPanelUsernameBytes || len(username) > maxPanelUsernameBytes {
		return "", errInvalidPanelAuthCredentials
	}
	if len(password) < minPanelPasswordBytes || len(password) > maxPanelPasswordBytes {
		return "", errInvalidPanelAuthCredentials
	}
	return username, nil
}

func loginFailureKey(username, clientIP string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "\x00" + clientIP
}

func writeRetryAfter(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}
