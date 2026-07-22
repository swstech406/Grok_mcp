package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
)

func trustedPanelClientIPResolver() *ratelimit.ClientIPResolver {
	return ratelimit.NewClientIPResolverWithConfig(ratelimit.ClientIPResolverConfig{
		Mode:                 ratelimit.ClientIPModeTrustedProxy,
		TrustedProxyPrefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	})
}

func TestAuthProtectorDirectModeLimitsHeaderlessRequestsAndIgnoresHeaders(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		RegisterIPRequestsPerMinute: 1,
		RegisterIPBurst:             1,
	})

	allowedRequestCount := 0
	protectedHandler := authProtector.RateLimitAuthEndpoint(authEndpointRegister, http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		allowedRequestCount++
		responseWriter.WriteHeader(http.StatusOK)
	}))

	for requestIndex := 0; requestIndex < 2; requestIndex++ {
		request := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/register", nil)
		request.RemoteAddr = "198.51.100.10:8443"
		request.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", requestIndex+1))
		responseRecorder := httptest.NewRecorder()
		protectedHandler.ServeHTTP(responseRecorder, request)
		expectedStatus := http.StatusOK
		if requestIndex == 1 {
			expectedStatus = http.StatusTooManyRequests
		}
		if responseRecorder.Code != expectedStatus {
			t.Fatalf("direct request %d status = %d, want %d", requestIndex+1, responseRecorder.Code, expectedStatus)
		}
	}

	if allowedRequestCount != 1 {
		t.Fatalf("allowed request count = %d, want %d", allowedRequestCount, 1)
	}
}

func TestAuthProtectorRejectsInvalidForwardedClientIPHeaders(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		ClientIPResolver:            trustedPanelClientIPResolver(),
		RegisterIPRequestsPerMinute: 1,
		RegisterIPBurst:             1,
	})

	allowedRequestCount := 0
	protectedHandler := authProtector.RateLimitAuthEndpoint(authEndpointRegister, http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		allowedRequestCount++
		responseWriter.WriteHeader(http.StatusOK)
	}))

	for requestIndex := 0; requestIndex < 2; requestIndex++ {
		request := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/register", nil)
		request.Header.Set("X-Real-IP", "not-an-ip")
		request.Header.Set("X-Forwarded-For", "unknown, invalid")
		responseRecorder := httptest.NewRecorder()
		protectedHandler.ServeHTTP(responseRecorder, request)
		if responseRecorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid-header request %d status = %d, want %d", requestIndex+1, responseRecorder.Code, http.StatusBadRequest)
		}
	}

	if allowedRequestCount != 0 {
		t.Fatalf("allowed request count = %d, want 0", allowedRequestCount)
	}
}

func TestAuthProtectorRejectsConflictingForwardedClientIPHeaders(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{ClientIPResolver: trustedPanelClientIPResolver()})
	protectedHandler := authProtector.RateLimitAuthEndpoint(authEndpointLogin, http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/login", nil)
	request.Header.Set("X-Real-IP", "198.51.100.10")
	request.Header.Set("X-Forwarded-For", "198.51.100.11")
	responseRecorder := httptest.NewRecorder()
	protectedHandler.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusBadRequest {
		t.Fatalf("conflicting-header request status = %d, want %d", responseRecorder.Code, http.StatusBadRequest)
	}
}

func TestAuthProtectorUsesFixedDefaultsAndClampsDirectCapacities(t *testing.T) {
	defaultMetrics := NewAuthProtector(AuthProtectorConfig{}).Metrics()
	if defaultMetrics.Login.MaximumEntries != defaultLoginIPMaximumEntries ||
		defaultMetrics.Register.MaximumEntries != defaultRegisterIPMaximumEntries ||
		defaultMetrics.RegistrationChallenge.MaximumEntries != defaultRegistrationChallengeIPMaximumEntries ||
		defaultMetrics.LoginFailures.MaximumEntries != defaultLoginFailureMaximumEntries {
		t.Fatalf("default auth protector capacities = %+v", defaultMetrics)
	}
	if defaultMetrics.Login.FallbackBucketCount != defaultAuthEndpointFallbackBuckets ||
		defaultMetrics.Register.FallbackBucketCount != defaultAuthEndpointFallbackBuckets ||
		defaultMetrics.RegistrationChallenge.FallbackBucketCount != defaultAuthEndpointFallbackBuckets {
		t.Fatalf("default fallback bucket counts = %+v", defaultMetrics)
	}

	clampedMetrics := NewAuthProtector(AuthProtectorConfig{
		LoginIPMaximumEntries:                 maximumAuthEndpointEntries + 1,
		RegisterIPMaximumEntries:              maximumAuthEndpointEntries + 1,
		RegistrationChallengeIPMaximumEntries: maximumAuthEndpointEntries + 1,
		LoginFailureMaximumEntries:            maximumLoginFailureEntries + 1,
		AuthEndpointFallbackBuckets:           maximumAuthEndpointFallbackBuckets + 1,
	}).Metrics()
	if clampedMetrics.Login.MaximumEntries != maximumAuthEndpointEntries ||
		clampedMetrics.Register.MaximumEntries != maximumAuthEndpointEntries ||
		clampedMetrics.RegistrationChallenge.MaximumEntries != maximumAuthEndpointEntries ||
		clampedMetrics.LoginFailures.MaximumEntries != maximumLoginFailureEntries {
		t.Fatalf("clamped auth protector capacities = %+v", clampedMetrics)
	}
	if clampedMetrics.Login.FallbackBucketCount != maximumAuthEndpointFallbackBuckets {
		t.Fatalf("clamped fallback bucket count = %d, want %d", clampedMetrics.Login.FallbackBucketCount, maximumAuthEndpointFallbackBuckets)
	}
}

func TestHandlerAuthProtectorSeparatesForwardedClientBuckets(t *testing.T) {
	handler := &Handler{AuthProtector: NewAuthProtector(AuthProtectorConfig{
		ClientIPResolver: trustedPanelClientIPResolver(),
	})}
	authProtector := handler.authProtector()

	allowedRequestCount := 0
	protectedHandler := authProtector.RateLimitAuthEndpoint(authEndpointRegister, http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		allowedRequestCount++
		responseWriter.WriteHeader(http.StatusOK)
	}))

	performRequest := func(forwardedClientIP string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/register", nil)
		request.RemoteAddr = "192.0.2.10:8443"
		request.Header.Set("X-Forwarded-For", forwardedClientIP)
		responseRecorder := httptest.NewRecorder()
		protectedHandler.ServeHTTP(responseRecorder, request)
		return responseRecorder
	}

	for requestIndex := 0; requestIndex < 10; requestIndex++ {
		responseRecorder := performRequest("198.51.100.10")
		if responseRecorder.Code != http.StatusOK {
			t.Fatalf("client A request %d status = %d, want %d", requestIndex+1, responseRecorder.Code, http.StatusOK)
		}
	}

	clientBResponse := performRequest("198.51.100.11")
	if clientBResponse.Code != http.StatusOK {
		t.Fatalf("client B should have a separate rate-limit bucket, status = %d", clientBResponse.Code)
	}

	limitedClientAResponse := performRequest("198.51.100.10")
	if limitedClientAResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("client A request after exhausting its bucket status = %d, want %d", limitedClientAResponse.Code, http.StatusTooManyRequests)
	}
	if allowedRequestCount != 11 {
		t.Fatalf("allowed request count = %d, want %d", allowedRequestCount, 11)
	}
}

func TestAuthProtectorBoundsDistributedUsernameGuessingAcrossClientIPs(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		ClientIPResolver:      trustedPanelClientIPResolver(),
		LoginFailureThreshold: 1,
		LoginBaseLockout:      time.Minute,
		LoginMaxLockout:       time.Minute,
	})

	clientARequest := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/login", nil)
	clientARequest.RemoteAddr = "192.0.2.10:8443"
	clientARequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	clientBRequest := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/login", nil)
	clientBRequest.RemoteAddr = "192.0.2.10:8443"
	clientBRequest.Header.Set("X-Forwarded-For", "198.51.100.11")

	clientAIP := authProtector.clientIP(clientARequest)
	clientBIP := authProtector.clientIP(clientBRequest)
	if clientAIP == clientBIP {
		t.Fatalf("forwarded clients resolved to the same IP %q", clientAIP)
	}

	clientAAttempt, retryAfter := authProtector.beginLoginAttempt("alice", clientAIP)
	if clientAAttempt == nil {
		t.Fatalf("client A initial attempt rejected with retry after %s", retryAfter)
	}
	clientAAttempt.recordFailure()
	if lockedAttempt, _ := authProtector.beginLoginAttempt("alice", clientAIP); lockedAttempt != nil {
		lockedAttempt.abandon()
		t.Fatalf("client A should be locked after reaching the failure threshold")
	}
	clientBAttempt, _ := authProtector.beginLoginAttempt("alice", clientBIP)
	if clientBAttempt != nil {
		clientBAttempt.abandon()
		t.Fatalf("source rotation must not bypass Alice's username-only lockout")
	}
	differentUsernameAttempt, _ := authProtector.beginLoginAttempt("bob", clientBIP)
	if differentUsernameAttempt == nil {
		t.Fatal("a different username should retain an independent failure budget")
	}
	differentUsernameAttempt.abandon()
}

func TestAuthProtectorUsernameFailureRegistryUsesBoundedFallback(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginFailureThreshold:          1,
		LoginBaseLockout:               time.Minute,
		LoginMaxLockout:                time.Minute,
		UsernameFailureMaximumEntries:  1,
		UsernameFailureFallbackBuckets: 1,
	})

	dedicatedAttempt, _ := authProtector.beginLoginAttempt("dedicated-user", "198.51.100.1")
	if dedicatedAttempt == nil {
		t.Fatal("dedicated username failure identity was rejected")
	}
	dedicatedAttempt.recordFailure()

	firstFallbackAttempt, _ := authProtector.beginLoginAttempt("fallback-user-one", "198.51.100.2")
	if firstFallbackAttempt == nil {
		t.Fatal("first overflow username should be admitted by the fallback budget")
	}
	firstFallbackAttempt.recordFailure()
	secondFallbackAttempt, _ := authProtector.beginLoginAttempt("fallback-user-two", "198.51.100.3")
	if secondFallbackAttempt != nil {
		secondFallbackAttempt.abandon()
		t.Fatal("shared fallback budget should reject after its lockout threshold")
	}

	metrics := authProtector.Metrics().UsernameFailures
	if metrics.CurrentEntries != 1 || metrics.MaximumEntries != 1 || metrics.FallbackBucketCount != 1 {
		t.Fatalf("username registry metrics = %+v", metrics)
	}
	if metrics.FallbackAttempts != 2 || metrics.FallbackRejections != 1 {
		t.Fatalf("username fallback metrics = %+v", metrics)
	}
}

func TestAuthProtectorUsernameInFlightAttemptsEnforceDistributedThreshold(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{LoginFailureThreshold: 2})
	firstAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.10")
	secondAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.11")
	if firstAttempt == nil || secondAttempt == nil {
		t.Fatal("the first two in-flight attempts should be admitted at threshold two")
	}
	thirdAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.12")
	if thirdAttempt != nil {
		thirdAttempt.abandon()
		t.Fatal("third source-rotated attempt exceeded the username in-flight threshold")
	}
	firstAttempt.abandon()
	secondAttempt.abandon()
	if metrics := authProtector.Metrics(); metrics.LoginFailures.CurrentEntries != 0 || metrics.UsernameFailures.CurrentEntries != 0 {
		t.Fatalf("abandoned attempt registries were not released: %+v", metrics)
	}
}

func TestAuthProtectorSuccessfulLoginClearsSourceAndUsernameFailureState(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{LoginFailureThreshold: 3})
	failedAttempt, _ := authProtector.beginLoginAttempt("Alice", "198.51.100.15")
	if failedAttempt == nil {
		t.Fatal("failed login attempt was not admitted")
	}
	failedAttempt.recordFailure()
	successfulAttempt, _ := authProtector.beginLoginAttempt(" alice ", "198.51.100.15")
	if successfulAttempt == nil {
		t.Fatal("successful retry was not admitted")
	}
	successfulAttempt.recordSuccess()

	metrics := authProtector.Metrics()
	if metrics.LoginFailures.CurrentEntries != 0 || metrics.UsernameFailures.CurrentEntries != 0 {
		t.Fatalf("successful login did not clear both failure identities: %+v", metrics)
	}
}

func TestAuthProtectorReclaimsExpiredUsernameFailureAtCapacity(t *testing.T) {
	currentTime := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginFailureThreshold:          1,
		LoginFailureWindow:             time.Minute,
		LoginBaseLockout:               time.Minute,
		LoginMaxLockout:                time.Minute,
		UsernameFailureMaximumEntries:  1,
		UsernameFailureFallbackBuckets: 1,
	})
	authProtector.now = func() time.Time { return currentTime }
	authProtector.lastCleanup = currentTime

	firstAttempt, _ := authProtector.beginLoginAttempt("expired-user", "198.51.100.20")
	if firstAttempt == nil {
		t.Fatal("first username attempt was rejected")
	}
	firstAttempt.recordFailure()
	currentTime = currentTime.Add(2 * time.Minute)

	secondAttempt, _ := authProtector.beginLoginAttempt("replacement-user", "198.51.100.21")
	if secondAttempt == nil {
		t.Fatal("expired username state was not reclaimed at capacity")
	}
	secondAttempt.abandon()
	metrics := authProtector.Metrics().UsernameFailures
	if metrics.FallbackAttempts != 0 || metrics.ExpiredEntriesRemoved != 1 {
		t.Fatalf("username expiry metrics = %+v", metrics)
	}
}

func TestAuthProtectorBoundsEndpointEntriesAndPreservesLiveBuckets(t *testing.T) {
	currentTime := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginIPRequestsPerMinute:              1,
		LoginIPBurst:                          1,
		RegisterIPRequestsPerMinute:           1,
		RegisterIPBurst:                       1,
		LoginIPMaximumEntries:                 1,
		RegisterIPMaximumEntries:              1,
		RegistrationChallengeIPMaximumEntries: 1,
		AuthEndpointFallbackBuckets:           1,
	})
	authProtector.now = func() time.Time { return currentTime }
	authProtector.lastCleanup = currentTime

	if allowed, _ := authProtector.allowAuthRequest(authEndpointLogin, "198.51.100.1"); !allowed {
		t.Fatal("first login IP should receive the dedicated bucket's initial token")
	}
	if allowed, _ := authProtector.allowAuthRequest(authEndpointLogin, "198.51.100.2"); !allowed {
		t.Fatal("first overflow login IP should receive the fallback bucket's initial token")
	}
	if allowed, _ := authProtector.allowAuthRequest(authEndpointLogin, "198.51.100.3"); allowed {
		t.Fatal("second overflow login IP should share the exhausted fallback bucket")
	}
	if allowed, _ := authProtector.allowAuthRequest(authEndpointLogin, "198.51.100.1"); allowed {
		t.Fatal("capacity pressure must not replace the exhausted dedicated bucket")
	}
	if allowed, _ := authProtector.allowAuthRequest(authEndpointRegister, "198.51.100.2"); !allowed {
		t.Fatal("login saturation must not consume the registration endpoint budget")
	}
	if allowed, _ := authProtector.allowAuthRequest(authEndpointRegistrationChallenge, "198.51.100.2"); !allowed {
		t.Fatal("registration saturation must not consume the challenge endpoint budget")
	}

	metrics := authProtector.Metrics()
	if metrics.Login.CurrentEntries != 1 || metrics.Login.MaximumEntries != 1 {
		t.Fatalf("login entry metrics = %+v, want current=1 maximum=1", metrics.Login)
	}
	if metrics.Login.FallbackRequests != 2 || metrics.Login.FallbackRejections != 1 {
		t.Fatalf("login fallback metrics = %+v, want requests=2 rejections=1", metrics.Login)
	}
	if metrics.Register.CurrentEntries != 1 {
		t.Fatalf("register current entries = %d, want 1", metrics.Register.CurrentEntries)
	}
	if metrics.RegistrationChallenge.CurrentEntries != 1 {
		t.Fatalf("registration challenge current entries = %d, want 1", metrics.RegistrationChallenge.CurrentEntries)
	}
}

func TestAuthProtectorFallbackSelectionIsDeterministicWithinProcess(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		AuthEndpointFallbackBuckets: 4,
	})
	const clientIP = "2001:db8::1234"
	firstIndex := authProtector.fallbackBucketIndexFor(clientIP, 4)
	for selectionIndex := 0; selectionIndex < 100; selectionIndex++ {
		if selectedIndex := authProtector.fallbackBucketIndexFor(clientIP, 4); selectedIndex != firstIndex {
			t.Fatalf("fallback index changed from %d to %d", firstIndex, selectedIndex)
		}
	}
}

func TestAuthProtectorReclaimsExpiredEndpointEntryAtCapacity(t *testing.T) {
	currentTime := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginIPMaximumEntries:       1,
		AuthEndpointFallbackBuckets: 1,
	})
	authProtector.now = func() time.Time { return currentTime }
	authProtector.lastCleanup = currentTime

	if allowed, _ := authProtector.allowAuthRequest(authEndpointLogin, "198.51.100.10"); !allowed {
		t.Fatal("initial dedicated request should be allowed")
	}
	currentTime = currentTime.Add(authRateLimiterIdleTTL)
	if allowed, _ := authProtector.allowAuthRequest(authEndpointLogin, "198.51.100.11"); !allowed {
		t.Fatal("new IP should receive a reclaimed dedicated slot at the exact expiry boundary")
	}

	metrics := authProtector.Metrics().Login
	if metrics.CurrentEntries != 1 || metrics.DedicatedAdmissions != 2 || metrics.ExpiredEntriesRemoved != 1 || metrics.FallbackRequests != 0 {
		t.Fatalf("login metrics after expiry reclaim = %+v", metrics)
	}
}

func TestAuthProtectorLoginFailureCapacityPreservesActiveLockout(t *testing.T) {
	currentTime := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginFailureMaximumEntries: 1,
		LoginFailureThreshold:      1,
		LoginFailureWindow:         time.Minute,
		LoginBaseLockout:           10 * time.Minute,
		LoginMaxLockout:            10 * time.Minute,
	})
	authProtector.now = func() time.Time { return currentTime }
	authProtector.lastCleanup = currentTime

	aliceAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.20")
	if aliceAttempt == nil {
		t.Fatal("alice should be admitted into an empty failure registry")
	}
	aliceAttempt.recordFailure()

	currentTime = currentTime.Add(2 * time.Minute)
	if bobAttempt, _ := authProtector.beginLoginAttempt("bob", "198.51.100.21"); bobAttempt != nil {
		bobAttempt.abandon()
		t.Fatal("active lockout must not be reclaimed after only the failure window expires")
	}
	if metrics := authProtector.Metrics().LoginFailures; metrics.CurrentEntries != 1 || metrics.CapacityRejections != 1 {
		t.Fatalf("failure metrics under active lockout = %+v", metrics)
	}

	currentTime = currentTime.Add(8 * time.Minute)
	bobAttempt, _ := authProtector.beginLoginAttempt("bob", "198.51.100.21")
	if bobAttempt == nil {
		t.Fatal("expired lockout should be reclaimed synchronously at capacity")
	}
	bobAttempt.abandon()
	if metrics := authProtector.Metrics().LoginFailures; metrics.CurrentEntries != 0 || metrics.Admissions != 2 || metrics.ExpiredEntriesRemoved != 1 {
		t.Fatalf("failure metrics after reclaim and abandonment = %+v", metrics)
	}
}

func TestAuthProtectorSkipsRepeatedFailureCleanupBeforeNextExpiry(t *testing.T) {
	currentTime := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginFailureMaximumEntries: 2,
		LoginFailureThreshold:      100,
		LoginFailureWindow:         10 * time.Minute,
	})
	authProtector.now = func() time.Time { return currentTime }
	authProtector.lastCleanup = currentTime

	for entryIndex := 0; entryIndex < 2; entryIndex++ {
		attempt, _ := authProtector.beginLoginAttempt(
			fmt.Sprintf("existing-user-%d", entryIndex),
			fmt.Sprintf("198.51.100.%d", entryIndex+60),
		)
		if attempt == nil {
			t.Fatalf("existing failure entry %d should be admitted", entryIndex)
		}
		attempt.recordFailure()
	}
	initialCleanupScans := authProtector.loginFailureCleanupScans.Load()
	for rejectedIndex := 0; rejectedIndex < 100; rejectedIndex++ {
		attempt, _ := authProtector.beginLoginAttempt(
			fmt.Sprintf("overflow-user-%d", rejectedIndex),
			fmt.Sprintf("2001:db8:1::%x", rejectedIndex+1),
		)
		if attempt != nil {
			attempt.abandon()
			t.Fatalf("overflow failure identity %d should be rejected", rejectedIndex)
		}
	}
	if cleanupScans := authProtector.loginFailureCleanupScans.Load(); cleanupScans != initialCleanupScans {
		t.Fatalf("cleanup scans before earliest expiry = %d, want %d", cleanupScans, initialCleanupScans)
	}

	currentTime = currentTime.Add(10 * time.Minute)
	reclaimedAttempt, _ := authProtector.beginLoginAttempt("reclaimed-user", "198.51.100.99")
	if reclaimedAttempt == nil {
		t.Fatal("new identity should be admitted after the earliest expiry")
	}
	reclaimedAttempt.abandon()
	if cleanupScans := authProtector.loginFailureCleanupScans.Load(); cleanupScans != initialCleanupScans+1 {
		t.Fatalf("cleanup scans after expiry = %d, want %d", cleanupScans, initialCleanupScans+1)
	}
}

func TestAuthProtectorCountsInFlightAttemptsTowardFailureThreshold(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginFailureThreshold: 2,
	})

	firstAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.70")
	secondAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.70")
	if firstAttempt == nil || secondAttempt == nil {
		t.Fatal("the first two concurrent attempts should consume the threshold budget")
	}
	if thirdAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.70"); thirdAttempt != nil {
		thirdAttempt.abandon()
		t.Fatal("an attempt beyond failure plus in-flight threshold must be rejected")
	}

	firstAttempt.abandon()
	replacementAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.70")
	if replacementAttempt == nil {
		t.Fatal("abandonment should release provisional threshold pressure")
	}
	replacementAttempt.recordFailure()
	secondAttempt.recordFailure()
	if unlockedAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.70"); unlockedAttempt != nil {
		unlockedAttempt.abandon()
		t.Fatal("completed failures at the threshold should lock the pair")
	}
}

func TestAuthProtectorAllowsFirstRetryAfterLockoutExpires(t *testing.T) {
	currentTime := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginFailureThreshold: 1,
		LoginFailureWindow:    15 * time.Minute,
		LoginBaseLockout:      time.Minute,
		LoginMaxLockout:       time.Minute,
	})
	authProtector.now = func() time.Time { return currentTime }
	authProtector.lastCleanup = currentTime

	failedAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.75")
	if failedAttempt == nil {
		t.Fatal("initial attempt should be admitted")
	}
	failedAttempt.recordFailure()
	currentTime = currentTime.Add(time.Minute)

	retryAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.75")
	if retryAttempt == nil {
		t.Fatal("the first retry after the completed lockout should be admitted")
	}
	if concurrentAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.75"); concurrentAttempt != nil {
		concurrentAttempt.abandon()
		t.Fatal("a concurrent retry must remain blocked at the failure threshold")
	}
	retryAttempt.abandon()
}

func TestLoginAttemptCompletionIsIdempotent(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginFailureThreshold: 2,
	})
	attempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.80")
	if attempt == nil {
		t.Fatal("login attempt should be admitted")
	}
	attempt.recordFailure()
	attempt.recordSuccess()
	attempt.abandon()

	entry := authProtector.failures[loginFailureKey("alice", "198.51.100.80")]
	if entry == nil || entry.failureCount != 1 || entry.inFlightAttempts != 0 {
		t.Fatalf("entry after repeated completion = %+v, want one completed failure", entry)
	}
	if currentEntries := authProtector.Metrics().LoginFailures.CurrentEntries; currentEntries != 1 {
		t.Fatalf("current failure entries = %d, want 1", currentEntries)
	}
}

func TestAuthProtectorNormalizesLoginFailureIdentity(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginFailureThreshold: 2,
	})

	firstAttempt, _ := authProtector.beginLoginAttempt(" Alice ", "198.51.100.30")
	if firstAttempt == nil {
		t.Fatal("first normalized identity attempt should be admitted")
	}
	firstAttempt.recordFailure()
	secondAttempt, _ := authProtector.beginLoginAttempt("alice", "198.51.100.30")
	if secondAttempt == nil {
		t.Fatal("second normalized identity attempt should use the existing entry")
	}
	secondAttempt.recordFailure()
	if unlockedAttempt, _ := authProtector.beginLoginAttempt("ALICE", "198.51.100.30"); unlockedAttempt != nil {
		unlockedAttempt.abandon()
		t.Fatal("normalized identity should be locked after two failures")
	}
	if currentEntries := authProtector.Metrics().LoginFailures.CurrentEntries; currentEntries != 1 {
		t.Fatalf("normalized identity current entries = %d, want 1", currentEntries)
	}
}

func TestAuthProtectorConcurrentAdmissionsRemainBounded(t *testing.T) {
	const (
		maximumEntries = 8
		requestCount   = 100
	)
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginIPMaximumEntries:       maximumEntries,
		LoginFailureMaximumEntries:  maximumEntries,
		AuthEndpointFallbackBuckets: 1,
	})

	var limiterWaitGroup sync.WaitGroup
	startLimiterRequests := make(chan struct{})
	for requestIndex := 0; requestIndex < requestCount; requestIndex++ {
		limiterWaitGroup.Add(1)
		go func(index int) {
			defer limiterWaitGroup.Done()
			<-startLimiterRequests
			authProtector.allowAuthRequest(authEndpointLogin, fmt.Sprintf("2001:db8::%x", index+1))
		}(requestIndex)
	}
	close(startLimiterRequests)
	limiterWaitGroup.Wait()
	if currentEntries := authProtector.Metrics().Login.CurrentEntries; currentEntries != maximumEntries {
		t.Fatalf("concurrent limiter entries = %d, want %d", currentEntries, maximumEntries)
	}

	startFailureAttempts := make(chan struct{})
	releaseFailureAttempts := make(chan struct{})
	var admittedWaitGroup sync.WaitGroup
	for requestIndex := 0; requestIndex < requestCount; requestIndex++ {
		admittedWaitGroup.Add(1)
		go func(index int) {
			defer admittedWaitGroup.Done()
			<-startFailureAttempts
			attempt, _ := authProtector.beginLoginAttempt(
				fmt.Sprintf("user-%d", index),
				fmt.Sprintf("198.51.100.%d", index+1),
			)
			if attempt != nil {
				<-releaseFailureAttempts
				attempt.abandon()
			}
		}(requestIndex)
	}
	close(startFailureAttempts)

	deadline := time.Now().Add(5 * time.Second)
	for authProtector.Metrics().LoginFailures.CurrentEntries < maximumEntries && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if currentEntries := authProtector.Metrics().LoginFailures.CurrentEntries; currentEntries != maximumEntries {
		close(releaseFailureAttempts)
		admittedWaitGroup.Wait()
		t.Fatalf("concurrent failure entries = %d, want %d", currentEntries, maximumEntries)
	}
	close(releaseFailureAttempts)
	admittedWaitGroup.Wait()
	if currentEntries := authProtector.Metrics().LoginFailures.CurrentEntries; currentEntries != 0 {
		t.Fatalf("failure entries after abandonment = %d, want 0", currentEntries)
	}
}
