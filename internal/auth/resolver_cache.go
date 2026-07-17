package auth

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

const (
	defaultAuthCacheTTL         = 30 * time.Second
	defaultAuthCacheMaxEntries  = 4096
	defaultAuthCacheShardCount  = 32
	defaultAuthNegativeCacheTTL = 2 * time.Second
	authCacheCleanupInterval    = time.Minute
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

// APIKeyCacheConfig controls the bounded authentication cache. Zero values use
// conservative defaults suitable for the MCP request path.
type APIKeyCacheConfig struct {
	TTL             time.Duration
	NegativeTTL     time.Duration
	CleanupInterval time.Duration
	MaxEntries      int
	ShardCount      int
}

// CachedAPIKeyResolver 缓存 MCP 鉴权链上的完整认证快照，避免热路径重复查询
// API key、用户与 tier。管理员修改鉴权数据后应调用 InvalidateAll。
type CachedAPIKeyResolver struct {
	st          APIKeyStore
	ttl         time.Duration
	negativeTTL time.Duration
	now         func() time.Time
	shards      []authCacheShard
	generation  atomic.Uint64
	stopCleanup chan struct{}
	cleanupDone chan struct{}
	closeOnce   sync.Once
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
		st:          st,
		ttl:         config.TTL,
		negativeTTL: config.NegativeTTL,
		now:         time.Now,
		shards:      make([]authCacheShard, config.ShardCount),
		stopCleanup: make(chan struct{}),
		cleanupDone: make(chan struct{}),
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
	now := c.now()
	shard := c.shardFor(keyHash)
	shard.mu.Lock()
	if entry, ok := shard.byHash[keyHash]; ok && now.Before(entry.until) {
		shard.recency.MoveToFront(entry.listElement)
		key := cloneAPIKey(entry.key)
		user := cloneAuthenticatedUser(entry.user)
		negative := entry.negative
		shard.mu.Unlock()
		if negative {
			return nil, nil, nil
		}
		return key, user, nil
	}
	if expiredEntry, ok := shard.byHash[keyHash]; ok {
		shard.removeEntry(expiredEntry)
	}
	loadGeneration := c.generation.Load()
	shard.mu.Unlock()

	key, err := c.st.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, nil, err
	}
	if key == nil {
		c.storeEntry(keyHash, nil, nil, c.negativeTTL, true, loadGeneration)
		return nil, nil, nil
	}
	user, err := LoadUserWithTierLimits(ctx, c.st, key.UserID)
	if err != nil {
		return nil, nil, err
	}

	c.storeEntry(keyHash, key, user, c.ttl, false, loadGeneration)
	return cloneAPIKey(key), cloneAuthenticatedUser(user), nil
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

	if c.generation.Load() != loadGeneration {
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
