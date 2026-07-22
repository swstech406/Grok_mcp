package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestIPLimiterDirectModeLimitsHeaderlessRequestsAndIgnoresSpoofedHeaders(t *testing.T) {
	limiter := NewIPLimiter(1)
	defer limiter.Close()

	allowedRequests := 0
	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		allowedRequests++
		w.WriteHeader(http.StatusOK)
	}))

	firstRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	firstRequest.RemoteAddr = "198.51.100.10:10001"
	firstRequest.Header.Set("X-Forwarded-For", "203.0.113.10")
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, firstRequest)
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	secondRequest.RemoteAddr = "198.51.100.10:10002"
	secondRequest.Header.Set("X-Forwarded-For", "203.0.113.11")
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, secondRequest)
	if secondRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("second direct request status = %d, want %d", secondRecorder.Code, http.StatusTooManyRequests)
	}

	if allowedRequests != 1 {
		t.Fatalf("allowed request count = %d, want %d", allowedRequests, 1)
	}
}

func TestIPLimiterRejectsEmptyOrMalformedForwardedClientIPHeaders(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute: 1,
		ClientIPResolver:  newTrustedProxyResolver(),
	})
	defer limiter.Close()

	allowedRequestCount := 0
	handler := limiter.Middleware()(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		allowedRequestCount++
		responseWriter.WriteHeader(http.StatusOK)
	}))

	performRequest := func(realIP, forwardedFor string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		request.Header["X-Real-Ip"] = []string{realIP}
		request.Header["X-Forwarded-For"] = []string{forwardedFor}
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, request)
		return responseRecorder
	}

	if responseRecorder := performRequest("", ""); responseRecorder.Code != http.StatusBadRequest {
		t.Fatalf("empty-header request status = %d, want %d", responseRecorder.Code, http.StatusBadRequest)
	}
	if responseRecorder := performRequest("not-an-ip", "unknown, invalid"); responseRecorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed-header request status = %d, want %d", responseRecorder.Code, http.StatusBadRequest)
	}
	if allowedRequestCount != 0 {
		t.Fatalf("allowed request count = %d, want 0", allowedRequestCount)
	}
}

func TestIPLimiterTrustedProxyUsesForwardedClientIP(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute: 1,
		ClientIPResolver:  newTrustedProxyResolver(),
	})
	defer limiter.Close()

	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	firstRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	firstRequest.RemoteAddr = "192.0.2.10:10001"
	firstRequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, firstRequest)
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	secondRequest.RemoteAddr = "192.0.2.10:10002"
	secondRequest.Header.Set("X-Forwarded-For", "198.51.100.11")
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, secondRequest)
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("different forwarded clients should use separate buckets, status = %d", secondRecorder.Code)
	}
}

func TestIPLimiterRequiresRealIPAndForwardedForToAgree(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute: 1,
		ClientIPResolver:  newTrustedProxyResolver(),
	})
	defer limiter.Close()

	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	firstRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	firstRequest.RemoteAddr = "192.0.2.10:10001"
	firstRequest.Header.Set("X-Real-IP", "198.51.100.10")
	firstRequest.Header.Set("X-Forwarded-For", "198.51.100.10, 203.0.113.20")
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, firstRequest)
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first forwarded request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	secondRequest.RemoteAddr = "192.0.2.10:10002"
	secondRequest.Header.Set("X-Forwarded-For", "198.51.100.20, 203.0.113.20")
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, secondRequest)
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("X-Forwarded-For client should get a separate bucket, status = %d", secondRecorder.Code)
	}

	thirdRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	thirdRequest.RemoteAddr = "192.0.2.10:10003"
	thirdRequest.Header.Set("X-Real-IP", "198.51.100.10")
	thirdRecorder := httptest.NewRecorder()
	handler.ServeHTTP(thirdRecorder, thirdRequest)
	if thirdRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("X-Real-IP should reuse the first bucket, status = %d", thirdRecorder.Code)
	}

	conflictingRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	conflictingRequest.Header.Set("X-Real-IP", "198.51.100.10")
	conflictingRequest.Header.Set("X-Forwarded-For", "198.51.100.20")
	conflictingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(conflictingRecorder, conflictingRequest)
	if conflictingRecorder.Code != http.StatusBadRequest {
		t.Fatalf("conflicting forwarded headers status = %d, want %d", conflictingRecorder.Code, http.StatusBadRequest)
	}
}

func TestIPLimiterCanonicalizesEquivalentAddressesIntoOneBucket(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute: 1,
		ClientIPResolver:  newTrustedProxyResolver(),
	})
	defer limiter.Close()

	handler := limiter.Middleware()(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))

	firstRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	firstRequest.Header.Set("X-Forwarded-For", "::ffff:198.51.100.10")
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, firstRequest)
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	secondRequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, secondRequest)
	if secondRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("canonical equivalent request status = %d, want %d", secondRecorder.Code, http.StatusTooManyRequests)
	}
}

func TestIPLimiterIncrementallyCleansConfiguredShardBatch(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute:     1,
		ShardCount:            4,
		EntryIdleTTL:          time.Minute,
		CleanupInterval:       time.Hour,
		CleanupShardBatchSize: 2,
	})
	defer limiter.Close()

	now := time.Now()
	expiredAt := now.Add(-2 * time.Minute)
	for shardIndex := range limiter.shards {
		address := netip.AddrFrom4([4]byte{198, 51, 100, byte(shardIndex + 1)})
		limiter.shards[shardIndex].entries[address] = &ipEntry{
			limiter:  limiter.newTokenBucket(),
			lastSeen: expiredAt,
		}
		limiter.shards[shardIndex].highWatermark = 1
	}

	limiter.cleanupNextShards(now)
	for shardIndex := range limiter.shards {
		entryCount := len(limiter.shards[shardIndex].entries)
		if shardIndex < 2 && entryCount != 0 {
			t.Fatalf("cleaned shard %d entry count = %d, want 0", shardIndex, entryCount)
		}
		if shardIndex >= 2 && entryCount != 1 {
			t.Fatalf("deferred shard %d entry count = %d, want 1", shardIndex, entryCount)
		}
	}
}

func TestIPLimiterRebuildsShardAfterHighWatermarkDrops(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute: 1,
		ShardCount:        1,
		EntryIdleTTL:      time.Minute,
		CleanupInterval:   time.Hour,
	})
	defer limiter.Close()

	now := time.Now()
	expiredAt := now.Add(-2 * time.Minute)
	shard := &limiter.shards[0]
	for addressIndex := 0; addressIndex < minimumShardHighWatermarkForRebuild; addressIndex++ {
		address := netip.AddrFrom4([4]byte{10, byte(addressIndex >> 8), byte(addressIndex), 1})
		shard.entries[address] = &ipEntry{
			limiter:  limiter.newTokenBucket(),
			lastSeen: expiredAt,
		}
	}
	shard.highWatermark = len(shard.entries)

	limiter.cleanupExpiredEntries(now)
	if len(shard.entries) != 0 {
		t.Fatalf("entry count after cleanup = %d, want 0", len(shard.entries))
	}
	if shard.highWatermark != 0 {
		t.Fatalf("high watermark after rebuild = %d, want 0", shard.highWatermark)
	}
}

func TestIPLimiterBoundsDedicatedEntriesAndUsesSharedFallback(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute:       1,
		ShardCount:              1,
		MaximumEntriesPerShard:  2,
		FallbackBucketsPerShard: 1,
		CleanupInterval:         time.Hour,
	})
	defer limiter.Close()

	now := time.Now()
	dedicatedAddresses := []netip.Addr{
		netip.MustParseAddr("198.51.100.1"),
		netip.MustParseAddr("198.51.100.2"),
	}
	for _, clientAddress := range dedicatedAddresses {
		if !limiter.allowAt(clientAddress, now) {
			t.Fatalf("first dedicated request for %s was rejected", clientAddress)
		}
	}

	firstFallbackAddress := netip.MustParseAddr("198.51.100.3")
	if !limiter.allowAt(firstFallbackAddress, now) {
		t.Fatalf("first fallback request was rejected")
	}
	secondFallbackAddress := netip.MustParseAddr("198.51.100.4")
	if limiter.allowAt(secondFallbackAddress, now) {
		t.Fatalf("second request sharing the exhausted fallback bucket was allowed")
	}

	if entryCount := len(limiter.shards[0].entries); entryCount != 2 {
		t.Fatalf("dedicated entry count = %d, want 2", entryCount)
	}
	metrics := limiter.Metrics()
	if metrics.CurrentEntries != 2 || metrics.MaximumEntries != 2 {
		t.Fatalf("entry metrics = %+v, want current=2 maximum=2", metrics)
	}
	if metrics.FallbackBucketCount != 1 || metrics.FallbackRequests != 2 || metrics.FallbackRejections != 1 {
		t.Fatalf("fallback metrics = %+v", metrics)
	}
}

func TestIPLimiterDoesNotEvictOrResetExistingEntryAtCapacity(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute:       1,
		ShardCount:              1,
		MaximumEntriesPerShard:  1,
		FallbackBucketsPerShard: 1,
		CleanupInterval:         time.Hour,
	})
	defer limiter.Close()

	now := time.Now()
	existingAddress := netip.MustParseAddr("198.51.100.10")
	if !limiter.allowAt(existingAddress, now) {
		t.Fatalf("first existing-address request was rejected")
	}
	if limiter.allowAt(existingAddress, now) {
		t.Fatalf("second existing-address request should exhaust its dedicated bucket")
	}

	overflowAddress := netip.MustParseAddr("198.51.100.11")
	if !limiter.allowAt(overflowAddress, now) {
		t.Fatalf("first overflow request was rejected")
	}
	if limiter.allowAt(existingAddress, now) {
		t.Fatalf("capacity pressure reset the existing dedicated bucket")
	}
	if _, exists := limiter.shards[0].entries[existingAddress]; !exists {
		t.Fatalf("existing address was evicted at capacity")
	}
	if _, exists := limiter.shards[0].entries[overflowAddress]; exists {
		t.Fatalf("overflow address was inserted into the dedicated registry")
	}
}

func TestIPLimiterHashFallbackBucketsShareOnlySelectedTokenState(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute:       1,
		ShardCount:              1,
		MaximumEntriesPerShard:  1,
		FallbackBucketsPerShard: 2,
		CleanupInterval:         time.Hour,
	})
	defer limiter.Close()

	now := time.Now()
	if !limiter.allowAt(netip.MustParseAddr("192.0.2.1"), now) {
		t.Fatalf("dedicated request was rejected")
	}

	addressesByFallback := make(map[int][]netip.Addr)
	for addressSuffix := 2; addressSuffix < 255; addressSuffix++ {
		clientAddress := netip.AddrFrom4([4]byte{192, 0, 2, byte(addressSuffix)})
		fallbackIndex := limiter.fallbackBucketIndexFor(clientAddress, 2)
		addressesByFallback[fallbackIndex] = append(addressesByFallback[fallbackIndex], clientAddress)
		if len(addressesByFallback[0]) >= 2 && len(addressesByFallback[1]) >= 1 {
			break
		}
	}
	if len(addressesByFallback[0]) < 2 || len(addressesByFallback[1]) < 1 {
		t.Fatalf("failed to find addresses covering both fallback buckets")
	}

	firstSharedAddress := addressesByFallback[0][0]
	secondSharedAddress := addressesByFallback[0][1]
	isolatedAddress := addressesByFallback[1][0]
	if !limiter.allowAt(firstSharedAddress, now) {
		t.Fatalf("first shared-bucket request was rejected")
	}
	if limiter.allowAt(secondSharedAddress, now) {
		t.Fatalf("colliding address did not share exhausted fallback token state")
	}
	if !limiter.allowAt(isolatedAddress, now) {
		t.Fatalf("request mapped to a different fallback bucket was rejected")
	}
}

func TestIPLimiterReclaimsExpiredEntryBeforeFallback(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute:       1,
		ShardCount:              1,
		EntryIdleTTL:            time.Minute,
		MaximumEntriesPerShard:  1,
		FallbackBucketsPerShard: 1,
		CleanupInterval:         time.Hour,
	})
	defer limiter.Close()

	now := time.Now()
	expiredAddress := netip.MustParseAddr("203.0.113.1")
	if !limiter.allowAt(expiredAddress, now.Add(-2*time.Minute)) {
		t.Fatalf("expired-address setup request was rejected")
	}
	replacementAddress := netip.MustParseAddr("203.0.113.2")
	if !limiter.allowAt(replacementAddress, now) {
		t.Fatalf("replacement request was rejected")
	}

	if _, exists := limiter.shards[0].entries[expiredAddress]; exists {
		t.Fatalf("expired address remained in the dedicated registry")
	}
	if _, exists := limiter.shards[0].entries[replacementAddress]; !exists {
		t.Fatalf("replacement address did not receive the reclaimed dedicated slot")
	}
	metrics := limiter.Metrics()
	if metrics.CurrentEntries != 1 || metrics.DedicatedAdmissions != 2 || metrics.ExpiredEntriesRemoved != 1 {
		t.Fatalf("expiry metrics = %+v", metrics)
	}
	if metrics.FallbackRequests != 0 {
		t.Fatalf("replacement request unexpectedly used fallback: %+v", metrics)
	}
}

func TestIPLimiterConcurrentAdmissionsRespectPerShardCapacity(t *testing.T) {
	const (
		shardCount              = 8
		maximumEntriesPerShard  = 8
		maximumDedicatedEntries = shardCount * maximumEntriesPerShard
		requestCount            = 512
	)
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute:       requestCount,
		ShardCount:              shardCount,
		MaximumEntriesPerShard:  maximumEntriesPerShard,
		FallbackBucketsPerShard: 4,
		CleanupInterval:         time.Hour,
	})
	defer limiter.Close()

	now := time.Now()
	var requestWaitGroup sync.WaitGroup
	requestWaitGroup.Add(requestCount)
	for addressIndex := 0; addressIndex < requestCount; addressIndex++ {
		clientAddress := netip.AddrFrom4([4]byte{10, 0, byte(addressIndex >> 8), byte(addressIndex)})
		go func() {
			defer requestWaitGroup.Done()
			limiter.allowAt(clientAddress, now)
		}()
	}
	requestWaitGroup.Wait()

	totalEntryCount := 0
	for shardIndex := range limiter.shards {
		entryCount := len(limiter.shards[shardIndex].entries)
		if entryCount > maximumEntriesPerShard {
			t.Fatalf("shard %d entry count = %d, exceeds %d", shardIndex, entryCount, maximumEntriesPerShard)
		}
		totalEntryCount += entryCount
	}
	if totalEntryCount != maximumDedicatedEntries {
		t.Fatalf("concurrent dedicated entry count = %d, want %d", totalEntryCount, maximumDedicatedEntries)
	}
	metrics := limiter.Metrics()
	if metrics.CurrentEntries != maximumDedicatedEntries || metrics.DedicatedAdmissions != maximumDedicatedEntries {
		t.Fatalf("concurrent admission metrics = %+v", metrics)
	}
	if metrics.FallbackRequests != requestCount-maximumDedicatedEntries {
		t.Fatalf("fallback requests = %d, want %d", metrics.FallbackRequests, requestCount-maximumDedicatedEntries)
	}
}

func TestIPLimiterSkipsFullShardExpiryScanBeforeEarliestExpiry(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute:       2,
		ShardCount:              1,
		EntryIdleTTL:            time.Minute,
		MaximumEntriesPerShard:  1,
		FallbackBucketsPerShard: 1,
		CleanupInterval:         time.Hour,
	})
	defer limiter.Close()

	now := time.Now()
	if !limiter.allowAt(netip.MustParseAddr("198.51.100.100"), now) {
		t.Fatalf("dedicated setup request was rejected")
	}
	shard := &limiter.shards[0]
	shard.highWatermark = minimumShardHighWatermarkForRebuild
	if !limiter.allowAt(netip.MustParseAddr("198.51.100.101"), now.Add(30*time.Second)) {
		t.Fatalf("fallback request was rejected")
	}
	if shard.highWatermark != minimumShardHighWatermarkForRebuild {
		t.Fatalf("full shard was scanned and rebuilt before any entry could expire")
	}
}

func TestIPLimiterCapsDirectConfigurationAtSafeMaximums(t *testing.T) {
	limiter := NewIPLimiterWithConfig(IPLimiterConfig{
		RequestsPerMinute:       1,
		ShardCount:              1,
		MaximumEntriesPerShard:  maximumIPLimiterEntriesPerShard + 1,
		FallbackBucketsPerShard: maximumIPLimiterFallbacksPerShard + 1,
		CleanupInterval:         time.Hour,
	})
	defer limiter.Close()

	metrics := limiter.Metrics()
	if metrics.MaximumEntries != maximumIPLimiterEntriesPerShard {
		t.Fatalf("maximum entries = %d, want %d", metrics.MaximumEntries, maximumIPLimiterEntriesPerShard)
	}
	if metrics.FallbackBucketCount != maximumIPLimiterFallbacksPerShard {
		t.Fatalf("fallback buckets = %d, want %d", metrics.FallbackBucketCount, maximumIPLimiterFallbacksPerShard)
	}
}
