package ratelimit

import (
	"errors"
	"hash/maphash"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultIPLimiterShardCount          = 64
	defaultIPLimiterEntryIdleTTL        = 5 * time.Minute
	defaultIPLimiterCleanupInterval     = 10 * time.Second
	defaultIPLimiterCleanupShardBatch   = 4
	defaultIPLimiterEntriesPerShard     = 2048
	defaultIPLimiterFallbacksPerShard   = 16
	maximumIPLimiterEntriesPerShard     = 65536
	maximumIPLimiterFallbacksPerShard   = 1024
	minimumShardHighWatermarkForRebuild = 256
	shardHighWatermarkRebuildDivisor    = 4
)

type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type ipLimiterShard struct {
	mu               sync.Mutex
	entries          map[netip.Addr]*ipEntry
	fallbackLimiters []*rate.Limiter
	maximumEntries   int
	highWatermark    int
	nextExpiryCheck  time.Time
}

// IPLimiterConfig controls the in-memory IP limiter. Zero values use bounded,
// production-oriented defaults.
type IPLimiterConfig struct {
	RequestsPerMinute       int
	ClientIPResolver        *ClientIPResolver
	ShardCount              int
	EntryIdleTTL            time.Duration
	CleanupInterval         time.Duration
	CleanupShardBatchSize   int
	MaximumEntriesPerShard  int
	FallbackBucketsPerShard int
}

// IPLimiterMetricsSnapshot reports bounded registry state without exposing IP
// addresses or individual token-bucket contents.
type IPLimiterMetricsSnapshot struct {
	CurrentEntries        int64  `json:"current_entries"`
	MaximumEntries        int64  `json:"maximum_entries"`
	FallbackBucketCount   int64  `json:"fallback_bucket_count"`
	DedicatedAdmissions   uint64 `json:"dedicated_admissions"`
	ExpiredEntriesRemoved uint64 `json:"expired_entries_removed"`
	FallbackRequests      uint64 `json:"fallback_requests"`
	FallbackRejections    uint64 `json:"fallback_rejections"`
}

// IPLimiter applies a shared token bucket to every request using the client
// identity established by its injected network-trust resolver.
type IPLimiter struct {
	requestsPerMinute     int
	clientIPResolver      *ClientIPResolver
	shards                []ipLimiterShard
	entryIdleTTL          time.Duration
	cleanupInterval       time.Duration
	cleanupShardBatchSize int
	cleanupCursor         int
	fallbackHashSeed      maphash.Seed
	currentEntries        atomic.Int64
	dedicatedAdmissions   atomic.Uint64
	expiredEntriesRemoved atomic.Uint64
	fallbackRequests      atomic.Uint64
	fallbackRejections    atomic.Uint64
	closeOnce             sync.Once
	stop                  chan struct{}
	workerDone            chan struct{}
}

// NewIPLimiter 创建来源 IP 限流器。
func NewIPLimiter(requestsPerMinute int) *IPLimiter {
	return NewIPLimiterWithConfig(IPLimiterConfig{RequestsPerMinute: requestsPerMinute})
}

// NewIPLimiterWithConfig creates a sharded limiter with incremental cleanup.
func NewIPLimiterWithConfig(config IPLimiterConfig) *IPLimiter {
	if config.RequestsPerMinute <= 0 {
		config.RequestsPerMinute = 60
	}
	if config.ClientIPResolver == nil {
		config.ClientIPResolver = NewClientIPResolver()
	}
	if config.ShardCount <= 0 {
		config.ShardCount = defaultIPLimiterShardCount
	}
	if config.EntryIdleTTL <= 0 {
		config.EntryIdleTTL = defaultIPLimiterEntryIdleTTL
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = defaultIPLimiterCleanupInterval
	}
	if config.CleanupShardBatchSize <= 0 {
		config.CleanupShardBatchSize = defaultIPLimiterCleanupShardBatch
	}
	if config.CleanupShardBatchSize > config.ShardCount {
		config.CleanupShardBatchSize = config.ShardCount
	}
	if config.MaximumEntriesPerShard <= 0 {
		config.MaximumEntriesPerShard = defaultIPLimiterEntriesPerShard
	}
	if config.MaximumEntriesPerShard > maximumIPLimiterEntriesPerShard {
		config.MaximumEntriesPerShard = maximumIPLimiterEntriesPerShard
	}
	if config.FallbackBucketsPerShard <= 0 {
		config.FallbackBucketsPerShard = defaultIPLimiterFallbacksPerShard
	}
	if config.FallbackBucketsPerShard > maximumIPLimiterFallbacksPerShard {
		config.FallbackBucketsPerShard = maximumIPLimiterFallbacksPerShard
	}

	limiter := &IPLimiter{
		requestsPerMinute:     config.RequestsPerMinute,
		clientIPResolver:      config.ClientIPResolver,
		shards:                make([]ipLimiterShard, config.ShardCount),
		entryIdleTTL:          config.EntryIdleTTL,
		cleanupInterval:       config.CleanupInterval,
		cleanupShardBatchSize: config.CleanupShardBatchSize,
		fallbackHashSeed:      maphash.MakeSeed(),
		stop:                  make(chan struct{}),
		workerDone:            make(chan struct{}),
	}
	for shardIndex := range limiter.shards {
		shard := &limiter.shards[shardIndex]
		shard.entries = make(map[netip.Addr]*ipEntry)
		shard.maximumEntries = config.MaximumEntriesPerShard
		shard.fallbackLimiters = make([]*rate.Limiter, config.FallbackBucketsPerShard)
		for fallbackIndex := range shard.fallbackLimiters {
			shard.fallbackLimiters[fallbackIndex] = limiter.newTokenBucket()
		}
	}
	go limiter.cleanupLoop()
	return limiter
}

func (limiter *IPLimiter) allow(clientAddress netip.Addr) bool {
	return limiter.allowAt(clientAddress, time.Now())
}

func (limiter *IPLimiter) allowAt(clientAddress netip.Addr, now time.Time) bool {
	shard := limiter.shardFor(clientAddress)
	shard.mu.Lock()
	entry, exists := shard.entries[clientAddress]
	if exists {
		entry.lastSeen = now
		limiter.recordPotentialExpiryLocked(shard, now)
		tokenBucket := entry.limiter
		shard.mu.Unlock()
		return tokenBucket.AllowN(now, 1)
	}

	shouldCheckForExpiredEntries := shard.nextExpiryCheck.IsZero() || now.After(shard.nextExpiryCheck)
	if len(shard.entries) >= shard.maximumEntries && shouldCheckForExpiredEntries {
		limiter.cleanupShardLocked(shard, now)
	}
	if len(shard.entries) < shard.maximumEntries {
		entry = &ipEntry{
			limiter:  limiter.newTokenBucket(),
			lastSeen: now,
		}
		shard.entries[clientAddress] = entry
		limiter.recordPotentialExpiryLocked(shard, now)
		if len(shard.entries) > shard.highWatermark {
			shard.highWatermark = len(shard.entries)
		}
		limiter.currentEntries.Add(1)
		limiter.dedicatedAdmissions.Add(1)
		tokenBucket := entry.limiter
		shard.mu.Unlock()
		return tokenBucket.AllowN(now, 1)
	}

	fallbackIndex := limiter.fallbackBucketIndexFor(clientAddress, len(shard.fallbackLimiters))
	tokenBucket := shard.fallbackLimiters[fallbackIndex]
	limiter.fallbackRequests.Add(1)
	shard.mu.Unlock()

	allowed := tokenBucket.AllowN(now, 1)
	if !allowed {
		limiter.fallbackRejections.Add(1)
	}
	return allowed
}

func (limiter *IPLimiter) newTokenBucket() *rate.Limiter {
	return rate.NewLimiter(rate.Every(time.Minute/time.Duration(limiter.requestsPerMinute)), limiter.requestsPerMinute)
}

func (limiter *IPLimiter) cleanupLoop() {
	defer close(limiter.workerDone)
	ticker := time.NewTicker(limiter.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-limiter.stop:
			return
		case now := <-ticker.C:
			limiter.cleanupNextShards(now)
		}
	}
}

func (limiter *IPLimiter) cleanupNextShards(now time.Time) {
	for cleanedShardCount := 0; cleanedShardCount < limiter.cleanupShardBatchSize; cleanedShardCount++ {
		limiter.cleanupShard(limiter.cleanupCursor, now)
		limiter.cleanupCursor = (limiter.cleanupCursor + 1) % len(limiter.shards)
	}
}

func (limiter *IPLimiter) cleanupExpiredEntries(now time.Time) {
	for shardIndex := range limiter.shards {
		limiter.cleanupShard(shardIndex, now)
	}
}

func (limiter *IPLimiter) cleanupShard(shardIndex int, now time.Time) {
	shard := &limiter.shards[shardIndex]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	limiter.cleanupShardLocked(shard, now)
}

func (limiter *IPLimiter) cleanupShardLocked(shard *ipLimiterShard, now time.Time) {
	cutoff := now.Add(-limiter.entryIdleTTL)
	removedEntryCount := 0
	nextExpiryCheck := time.Time{}
	for clientAddress, entry := range shard.entries {
		if entry.lastSeen.Before(cutoff) {
			delete(shard.entries, clientAddress)
			removedEntryCount++
			continue
		}
		entryExpiry := entry.lastSeen.Add(limiter.entryIdleTTL)
		if nextExpiryCheck.IsZero() || entryExpiry.Before(nextExpiryCheck) {
			nextExpiryCheck = entryExpiry
		}
	}
	shard.nextExpiryCheck = nextExpiryCheck
	if removedEntryCount > 0 {
		limiter.currentEntries.Add(-int64(removedEntryCount))
		limiter.expiredEntriesRemoved.Add(uint64(removedEntryCount))
	}

	shouldRebuild := shard.highWatermark >= minimumShardHighWatermarkForRebuild &&
		len(shard.entries) <= shard.highWatermark/shardHighWatermarkRebuildDivisor
	if !shouldRebuild {
		return
	}
	rebuiltEntries := make(map[netip.Addr]*ipEntry, len(shard.entries))
	for clientAddress, entry := range shard.entries {
		rebuiltEntries[clientAddress] = entry
	}
	shard.entries = rebuiltEntries
	shard.highWatermark = len(rebuiltEntries)
}

func (limiter *IPLimiter) recordPotentialExpiryLocked(shard *ipLimiterShard, lastSeen time.Time) {
	entryExpiry := lastSeen.Add(limiter.entryIdleTTL)
	if shard.nextExpiryCheck.IsZero() || entryExpiry.Before(shard.nextExpiryCheck) {
		shard.nextExpiryCheck = entryExpiry
	}
}

func (limiter *IPLimiter) fallbackBucketIndexFor(clientAddress netip.Addr, fallbackBucketCount int) int {
	addressBytes := clientAddress.As16()
	addressHash := maphash.Bytes(limiter.fallbackHashSeed, addressBytes[:])
	return int(addressHash % uint64(fallbackBucketCount))
}

func (limiter *IPLimiter) shardFor(clientAddress netip.Addr) *ipLimiterShard {
	return &limiter.shards[limiter.shardIndexFor(clientAddress)]
}

func (limiter *IPLimiter) shardIndexFor(clientAddress netip.Addr) int {
	const (
		fnvOffsetBasis uint64 = 1469598103934665603
		fnvPrime       uint64 = 1099511628211
	)
	addressBytes := clientAddress.As16()
	hash := fnvOffsetBasis
	for _, addressByte := range addressBytes {
		hash ^= uint64(addressByte)
		hash *= fnvPrime
	}
	return int(hash % uint64(len(limiter.shards)))
}

// Metrics returns a lock-free snapshot of registry capacity and saturation.
func (limiter *IPLimiter) Metrics() IPLimiterMetricsSnapshot {
	if limiter == nil {
		return IPLimiterMetricsSnapshot{}
	}
	shardCount := int64(len(limiter.shards))
	maximumEntriesPerShard := int64(0)
	fallbackBucketsPerShard := int64(0)
	if len(limiter.shards) > 0 {
		maximumEntriesPerShard = int64(limiter.shards[0].maximumEntries)
		fallbackBucketsPerShard = int64(len(limiter.shards[0].fallbackLimiters))
	}
	return IPLimiterMetricsSnapshot{
		CurrentEntries:        limiter.currentEntries.Load(),
		MaximumEntries:        shardCount * maximumEntriesPerShard,
		FallbackBucketCount:   shardCount * fallbackBucketsPerShard,
		DedicatedAdmissions:   limiter.dedicatedAdmissions.Load(),
		ExpiredEntriesRemoved: limiter.expiredEntriesRemoved.Load(),
		FallbackRequests:      limiter.fallbackRequests.Load(),
		FallbackRejections:    limiter.fallbackRejections.Load(),
	}
}

// Close 停止后台清理。
func (limiter *IPLimiter) Close() {
	limiter.closeOnce.Do(func() {
		close(limiter.stop)
		<-limiter.workerDone
	})
}

// Middleware rejects incomplete network identity before applying IP limits.
func (limiter *IPLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientAddress, err := limiter.clientIPResolver.ResolveAddress(r)
			if err != nil {
				if errors.Is(err, ErrUntrustedClientIPPeer) {
					http.Error(w, ErrUntrustedClientIPPeer.Error(), http.StatusForbidden)
					return
				}
				http.Error(w, ErrInvalidClientIPIdentity.Error(), http.StatusBadRequest)
				return
			}
			if !clientAddress.IsValid() {
				http.Error(w, ErrInvalidClientIPIdentity.Error(), http.StatusBadRequest)
				return
			}
			if !limiter.allow(clientAddress) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (limiter *IPLimiter) clientIP(r *http.Request) string {
	return limiter.clientIPResolver.Resolve(r)
}
