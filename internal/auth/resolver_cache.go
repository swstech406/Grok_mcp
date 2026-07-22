package auth

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

const (
	defaultAuthCacheTTL          = 30 * time.Second
	defaultAuthCacheMaxEntries   = 4096
	defaultAuthCacheShardCount   = 32
	defaultAuthNegativeCacheTTL  = 2 * time.Second
	defaultAuthMissMaxConcurrent = 32
	maximumAuthMissMaxConcurrent = 1024
	authCacheCleanupInterval     = time.Minute
)

var (
	// ErrAPIKeyResolverSaturated identifies fail-fast rejection before storage work.
	ErrAPIKeyResolverSaturated = errors.New("API key resolver is saturated")
	// ErrAPIKeyResolverClosed identifies use after resolver shutdown.
	ErrAPIKeyResolverClosed         = errors.New("API key resolver is closed")
	errAPIKeyResolverLeaderPanicked = errors.New("API key resolver load panicked")
)

type cacheEntry struct {
	keyHash     string
	key         *store.APIKey
	user        *AuthenticatedUser
	until       time.Time
	listElement *list.Element
	negative    bool
}

type authCacheShard struct {
	mu         sync.Mutex
	byHash     map[string]*cacheEntry
	recency    list.List
	maxEntries int
}

type apiKeyResolveFlight struct {
	done chan struct{}
	once sync.Once
	key  *store.APIKey
	user *AuthenticatedUser
	err  error
}

// APIKeyCacheConfig controls the bounded authentication cache. Zero values use
// conservative defaults suitable for the MCP request path.
type APIKeyCacheConfig struct {
	TTL               time.Duration
	NegativeTTL       time.Duration
	CleanupInterval   time.Duration
	MaxEntries        int
	ShardCount        int
	MissMaxConcurrent int
}

// CachedAPIKeyResolver 缓存 MCP 鉴权链上的完整认证快照，避免热路径重复查询
// API key、用户与 tier。管理员修改鉴权数据后应调用 InvalidateAll。
type CachedAPIKeyResolver struct {
	st            APIKeyStore
	ttl           time.Duration
	negativeTTL   time.Duration
	now           func() time.Time
	shards        []authCacheShard
	generation    atomic.Uint64
	flightMu      sync.Mutex
	inFlight      map[string]*apiKeyResolveFlight
	missAdmission chan struct{}
	closed        atomic.Bool
	stopCleanup   chan struct{}
	cleanupDone   chan struct{}
	closeOnce     sync.Once
}

// NewCachedAPIKeyResolver 创建鉴权解析缓存；ttl<=0 时使用默认 30s。
func NewCachedAPIKeyResolver(st APIKeyStore, ttl time.Duration) *CachedAPIKeyResolver {
	return NewCachedAPIKeyResolverWithConfig(st, APIKeyCacheConfig{TTL: ttl})
}

// NewCachedAPIKeyResolverWithConfig creates a sharded, capacity-bounded cache.
// Unknown hashes are cached briefly to avoid repeated storage work for the same
// invalid credential. Call Close when the resolver is no longer needed.
func NewCachedAPIKeyResolverWithConfig(st APIKeyStore, config APIKeyCacheConfig) *CachedAPIKeyResolver {
	config = normalizeAPIKeyCacheConfig(config)
	resolver := &CachedAPIKeyResolver{
		st:            st,
		ttl:           config.TTL,
		negativeTTL:   config.NegativeTTL,
		now:           time.Now,
		shards:        make([]authCacheShard, config.ShardCount),
		inFlight:      make(map[string]*apiKeyResolveFlight),
		missAdmission: make(chan struct{}, config.MissMaxConcurrent),
		stopCleanup:   make(chan struct{}),
		cleanupDone:   make(chan struct{}),
	}

	baseShardCapacity := config.MaxEntries / config.ShardCount
	shardsWithExtraEntry := config.MaxEntries % config.ShardCount
	for shardIndex := range resolver.shards {
		shardCapacity := baseShardCapacity
		if shardIndex < shardsWithExtraEntry {
			shardCapacity++
		}
		resolver.shards[shardIndex] = authCacheShard{
			byHash:     make(map[string]*cacheEntry, shardCapacity),
			maxEntries: shardCapacity,
		}
	}

	go resolver.runCleanup(config.CleanupInterval)
	return resolver
}

// Resolve 按 API Key 哈希加载密钥与启用用户（含 tier 限额）。
func (c *CachedAPIKeyResolver) Resolve(ctx context.Context, keyHash string) (*store.APIKey, *AuthenticatedUser, error) {
	if c.closed.Load() {
		return nil, nil, ErrAPIKeyResolverClosed
	}
	if key, user, found := c.loadCachedEntry(keyHash, c.now()); found {
		return key, user, nil
	}

	c.flightMu.Lock()
	if c.closed.Load() {
		c.flightMu.Unlock()
		return nil, nil, ErrAPIKeyResolverClosed
	}
	if existingFlight := c.inFlight[keyHash]; existingFlight != nil {
		c.flightMu.Unlock()
		return waitForAPIKeyResolveFlight(ctx, existingFlight)
	}
	// A previous leader may have populated the cache between the first cache
	// check and acquiring the flight lock.
	if key, user, found := c.loadCachedEntry(keyHash, c.now()); found {
		c.flightMu.Unlock()
		return key, user, nil
	}

	flight := &apiKeyResolveFlight{done: make(chan struct{})}
	c.inFlight[keyHash] = flight
	loadGeneration := c.generation.Load()
	c.flightMu.Unlock()

	select {
	case c.missAdmission <- struct{}{}:
	case <-ctx.Done():
		c.completeFlight(keyHash, flight, nil, nil, ctx.Err())
		return nil, nil, ctx.Err()
	default:
		c.completeFlight(keyHash, flight, nil, nil, ErrAPIKeyResolverSaturated)
		return nil, nil, ErrAPIKeyResolverSaturated
	}

	var key *store.APIKey
	var user *AuthenticatedUser
	var loadErr error
	func() {
		defer func() {
			<-c.missAdmission
			if recoveredValue := recover(); recoveredValue != nil {
				c.completeFlight(keyHash, flight, nil, nil, errAPIKeyResolverLeaderPanicked)
				panic(recoveredValue)
			}
		}()
		key, user, loadErr = c.loadFromStore(ctx, keyHash)
	}()

	if loadErr == nil && !c.closed.Load() {
		if key == nil {
			c.storeEntry(keyHash, nil, nil, c.negativeTTL, true, loadGeneration)
		} else {
			c.storeEntry(keyHash, key, user, c.ttl, false, loadGeneration)
		}
	}
	c.completeFlight(keyHash, flight, key, user, loadErr)
	return waitForAPIKeyResolveFlight(ctx, flight)
}

func (c *CachedAPIKeyResolver) loadCachedEntry(keyHash string, now time.Time) (*store.APIKey, *AuthenticatedUser, bool) {
	shard := c.shardFor(keyHash)
	shard.mu.Lock()
	if entry, ok := shard.byHash[keyHash]; ok && now.Before(entry.until) {
		shard.recency.MoveToFront(entry.listElement)
		key := cloneAPIKey(entry.key)
		user := cloneAuthenticatedUser(entry.user)
		negative := entry.negative
		shard.mu.Unlock()
		if negative {
			return nil, nil, true
		}
		return key, user, true
	}
	if expiredEntry, ok := shard.byHash[keyHash]; ok {
		shard.removeEntry(expiredEntry)
	}
	shard.mu.Unlock()
	return nil, nil, false
}

func (c *CachedAPIKeyResolver) loadFromStore(ctx context.Context, keyHash string) (*store.APIKey, *AuthenticatedUser, error) {
	key, err := c.st.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, nil, err
	}
	if key == nil {
		return nil, nil, nil
	}
	user, err := LoadUserWithTierLimits(ctx, c.st, key.UserID)
	if err != nil {
		return nil, nil, err
	}

	return key, user, nil
}

func waitForAPIKeyResolveFlight(ctx context.Context, flight *apiKeyResolveFlight) (*store.APIKey, *AuthenticatedUser, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-flight.done:
		return cloneAPIKey(flight.key), cloneAuthenticatedUser(flight.user), flight.err
	}
}

func (c *CachedAPIKeyResolver) completeFlight(
	keyHash string,
	flight *apiKeyResolveFlight,
	key *store.APIKey,
	user *AuthenticatedUser,
	err error,
) {
	flight.once.Do(func() {
		flight.key = cloneAPIKey(key)
		flight.user = cloneAuthenticatedUser(user)
		flight.err = err
		close(flight.done)

		c.flightMu.Lock()
		if c.inFlight[keyHash] == flight {
			delete(c.inFlight, keyHash)
		}
		c.flightMu.Unlock()
	})
}

func normalizeAPIKeyCacheConfig(config APIKeyCacheConfig) APIKeyCacheConfig {
	if config.TTL <= 0 {
		config.TTL = defaultAuthCacheTTL
	}
	if config.NegativeTTL <= 0 {
		config.NegativeTTL = defaultAuthNegativeCacheTTL
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = authCacheCleanupInterval
	}
	if config.MaxEntries <= 0 {
		config.MaxEntries = defaultAuthCacheMaxEntries
	}
	if config.ShardCount <= 0 {
		config.ShardCount = defaultAuthCacheShardCount
	}
	if config.ShardCount > config.MaxEntries {
		config.ShardCount = config.MaxEntries
	}
	if config.MissMaxConcurrent <= 0 {
		config.MissMaxConcurrent = defaultAuthMissMaxConcurrent
	}
	if config.MissMaxConcurrent > maximumAuthMissMaxConcurrent {
		config.MissMaxConcurrent = maximumAuthMissMaxConcurrent
	}
	return config
}

func (c *CachedAPIKeyResolver) shardFor(keyHash string) *authCacheShard {
	const fnvOffsetBasis uint64 = 14695981039346656037
	const fnvPrime uint64 = 1099511628211

	hashValue := uint64(fnvOffsetBasis)
	for characterIndex := 0; characterIndex < len(keyHash); characterIndex++ {
		hashValue ^= uint64(keyHash[characterIndex])
		hashValue *= fnvPrime
	}
	return &c.shards[hashValue%uint64(len(c.shards))]
}

func (c *CachedAPIKeyResolver) storeEntry(
	keyHash string,
	key *store.APIKey,
	user *AuthenticatedUser,
	ttl time.Duration,
	negative bool,
	loadGeneration uint64,
) {
	shard := c.shardFor(keyHash)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	if c.closed.Load() || c.generation.Load() != loadGeneration {
		return
	}
	if existingEntry, ok := shard.byHash[keyHash]; ok {
		shard.removeEntry(existingEntry)
	}

	entry := &cacheEntry{
		keyHash:  keyHash,
		key:      cloneAPIKey(key),
		user:     cloneAuthenticatedUser(user),
		until:    c.now().Add(ttl),
		negative: negative,
	}
	entry.listElement = shard.recency.PushFront(entry)
	shard.byHash[keyHash] = entry

	for len(shard.byHash) > shard.maxEntries {
		leastRecentElement := shard.recency.Back()
		if leastRecentElement == nil {
			break
		}
		shard.removeEntry(leastRecentElement.Value.(*cacheEntry))
	}
}

func (s *authCacheShard) removeEntry(entry *cacheEntry) {
	delete(s.byHash, entry.keyHash)
	s.recency.Remove(entry.listElement)
}

func (c *CachedAPIKeyResolver) runCleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(c.cleanupDone)

	for {
		select {
		case <-ticker.C:
			c.removeExpiredEntries(c.now())
		case <-c.stopCleanup:
			return
		}
	}
}

func (c *CachedAPIKeyResolver) removeExpiredEntries(now time.Time) {
	for shardIndex := range c.shards {
		shard := &c.shards[shardIndex]
		shard.mu.Lock()
		for keyHash, entry := range shard.byHash {
			if !now.Before(entry.until) {
				delete(shard.byHash, keyHash)
				shard.recency.Remove(entry.listElement)
			}
		}
		shard.mu.Unlock()
	}
}

// InvalidateAll 清空缓存（管理员变更 tier/用户/密钥后调用）。
func (c *CachedAPIKeyResolver) InvalidateAll() {
	c.generation.Add(1)
	for shardIndex := range c.shards {
		shard := &c.shards[shardIndex]
		shard.mu.Lock()
		clear(shard.byHash)
		shard.recency.Init()
		shard.mu.Unlock()
	}
}

// Close stops the background expiration worker. It is safe to call repeatedly.
func (c *CachedAPIKeyResolver) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.generation.Add(1)
		c.flightMu.Lock()
		activeFlights := make(map[string]*apiKeyResolveFlight, len(c.inFlight))
		for keyHash, flight := range c.inFlight {
			activeFlights[keyHash] = flight
		}
		c.flightMu.Unlock()
		for keyHash, flight := range activeFlights {
			c.completeFlight(keyHash, flight, nil, nil, ErrAPIKeyResolverClosed)
		}
		close(c.stopCleanup)
		<-c.cleanupDone
	})
}

func cloneAPIKey(key *store.APIKey) *store.APIKey {
	if key == nil {
		return nil
	}
	keyCopy := *key
	return &keyCopy
}

func cloneAuthenticatedUser(user *AuthenticatedUser) *AuthenticatedUser {
	if user == nil {
		return nil
	}
	userCopy := *user
	return &userCopy
}
