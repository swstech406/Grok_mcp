package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

type cachedResolverStore struct {
	store.TestStore
	key          *store.APIKey
	keys         map[string]*store.APIKey
	user         *store.User
	tier         *store.Tier
	getKeyCalls  int
	getUserCalls int
	getTierCalls int
}

type blockingCachedResolverStore struct {
	store.TestStore
	key         *store.APIKey
	user        *store.User
	tier        *store.Tier
	loadError   error
	loadStarted chan string
	releaseLoad chan struct{}
	loadCalls   atomic.Int64
}

func (resolverStore *blockingCachedResolverStore) GetKeyByHash(ctx context.Context, keyHash string) (*store.APIKey, error) {
	resolverStore.loadCalls.Add(1)
	resolverStore.loadStarted <- keyHash
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-resolverStore.releaseLoad:
	}
	if resolverStore.loadError != nil {
		return nil, resolverStore.loadError
	}
	if resolverStore.key == nil {
		return nil, nil
	}
	keyCopy := *resolverStore.key
	return &keyCopy, nil
}

func (resolverStore *blockingCachedResolverStore) GetUserByID(context.Context, string) (*store.User, error) {
	userCopy := *resolverStore.user
	return &userCopy, nil
}

func (resolverStore *blockingCachedResolverStore) GetTierByID(context.Context, string) (*store.Tier, error) {
	tierCopy := *resolverStore.tier
	return &tierCopy, nil
}

func newBlockingCachedResolverStore() *blockingCachedResolverStore {
	return &blockingCachedResolverStore{
		key:         &store.APIKey{ID: "key-id", UserID: "user-id", Enabled: true},
		user:        &store.User{ID: "user-id", TierID: "tier-id", Enabled: true},
		tier:        &store.Tier{ID: "tier-id", RPM: 10, SuccessLimit: 20},
		loadStarted: make(chan string, 128),
		releaseLoad: make(chan struct{}),
	}
}

func TestCachedAPIKeyResolverCoalescesSameHashMisses(t *testing.T) {
	resolverStore := newBlockingCachedResolverStore()
	resolver := NewCachedAPIKeyResolverWithConfig(resolverStore, APIKeyCacheConfig{
		MissMaxConcurrent: 2,
	})
	t.Cleanup(resolver.Close)

	type resolveResult struct {
		key *store.APIKey
		err error
	}
	results := make(chan resolveResult, 32)
	go func() {
		key, _, err := resolver.Resolve(context.Background(), "shared-hash")
		results <- resolveResult{key: key, err: err}
	}()
	if startedHash := <-resolverStore.loadStarted; startedHash != "shared-hash" {
		t.Fatalf("started hash = %q", startedHash)
	}
	for followerIndex := 0; followerIndex < 31; followerIndex++ {
		go func() {
			key, _, err := resolver.Resolve(context.Background(), "shared-hash")
			results <- resolveResult{key: key, err: err}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(resolverStore.releaseLoad)

	for resultIndex := 0; resultIndex < 32; resultIndex++ {
		result := <-results
		if result.err != nil || result.key == nil || result.key.ID != "key-id" {
			t.Fatalf("resolve result = key:%+v err:%v", result.key, result.err)
		}
		result.key.Enabled = false
	}
	if loadCalls := resolverStore.loadCalls.Load(); loadCalls != 1 {
		t.Fatalf("store load calls = %d, want 1", loadCalls)
	}
}

func TestCachedAPIKeyResolverRejectsDistinctHashWhenMissAdmissionIsFull(t *testing.T) {
	resolverStore := newBlockingCachedResolverStore()
	resolver := NewCachedAPIKeyResolverWithConfig(resolverStore, APIKeyCacheConfig{MissMaxConcurrent: 1})
	t.Cleanup(resolver.Close)

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := resolver.Resolve(context.Background(), "first-hash")
		firstDone <- err
	}()
	<-resolverStore.loadStarted

	_, _, err := resolver.Resolve(context.Background(), "second-hash")
	if !errors.Is(err, ErrAPIKeyResolverSaturated) {
		t.Fatalf("second miss error = %v, want saturation", err)
	}
	if loadCalls := resolverStore.loadCalls.Load(); loadCalls != 1 {
		t.Fatalf("store load calls before release = %d, want 1", loadCalls)
	}
	close(resolverStore.releaseLoad)
	if err := <-firstDone; err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}
}

func TestCachedAPIKeyResolverFollowerHonorsContextCancellation(t *testing.T) {
	resolverStore := newBlockingCachedResolverStore()
	resolver := NewCachedAPIKeyResolverWithConfig(resolverStore, APIKeyCacheConfig{MissMaxConcurrent: 1})
	t.Cleanup(resolver.Close)

	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := resolver.Resolve(context.Background(), "shared-hash")
		leaderDone <- err
	}()
	<-resolverStore.loadStarted

	followerContext, cancelFollower := context.WithCancel(context.Background())
	cancelFollower()
	_, _, err := resolver.Resolve(followerContext, "shared-hash")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("follower error = %v, want context cancellation", err)
	}
	close(resolverStore.releaseLoad)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader resolve failed: %v", err)
	}
}

func TestCachedAPIKeyResolverSharesLeaderFailure(t *testing.T) {
	resolverStore := newBlockingCachedResolverStore()
	resolverStore.loadError = errors.New("store unavailable")
	resolver := NewCachedAPIKeyResolverWithConfig(resolverStore, APIKeyCacheConfig{MissMaxConcurrent: 1})
	t.Cleanup(resolver.Close)

	results := make(chan error, 2)
	go func() {
		_, _, err := resolver.Resolve(context.Background(), "shared-hash")
		results <- err
	}()
	<-resolverStore.loadStarted
	go func() {
		_, _, err := resolver.Resolve(context.Background(), "shared-hash")
		results <- err
	}()
	time.Sleep(10 * time.Millisecond)
	close(resolverStore.releaseLoad)

	for resultIndex := 0; resultIndex < 2; resultIndex++ {
		if err := <-results; err == nil || err.Error() != "store unavailable" {
			t.Fatalf("shared leader error = %v", err)
		}
	}
	if loadCalls := resolverStore.loadCalls.Load(); loadCalls != 1 {
		t.Fatalf("store load calls = %d, want 1", loadCalls)
	}
}

func TestCachedAPIKeyResolverDoesNotCacheLoadInvalidatedInFlight(t *testing.T) {
	resolverStore := newBlockingCachedResolverStore()
	resolver := NewCachedAPIKeyResolverWithConfig(resolverStore, APIKeyCacheConfig{MissMaxConcurrent: 1})
	t.Cleanup(resolver.Close)

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := resolver.Resolve(context.Background(), "shared-hash")
		firstDone <- err
	}()
	<-resolverStore.loadStarted
	resolver.InvalidateAll()
	close(resolverStore.releaseLoad)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}

	if _, _, err := resolver.Resolve(context.Background(), "shared-hash"); err != nil {
		t.Fatal(err)
	}
	if loadCalls := resolverStore.loadCalls.Load(); loadCalls != 2 {
		t.Fatalf("store load calls = %d, want 2 after in-flight invalidation", loadCalls)
	}
}

func TestCachedAPIKeyResolverCloseReleasesFollowersAndPreventsNewWork(t *testing.T) {
	resolverStore := newBlockingCachedResolverStore()
	resolver := NewCachedAPIKeyResolverWithConfig(resolverStore, APIKeyCacheConfig{MissMaxConcurrent: 1})

	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := resolver.Resolve(context.Background(), "shared-hash")
		leaderDone <- err
	}()
	<-resolverStore.loadStarted
	followerDone := make(chan error, 1)
	go func() {
		_, _, err := resolver.Resolve(context.Background(), "shared-hash")
		followerDone <- err
	}()
	time.Sleep(10 * time.Millisecond)
	resolver.Close()

	if err := <-followerDone; !errors.Is(err, ErrAPIKeyResolverClosed) {
		t.Fatalf("follower error after close = %v", err)
	}
	if _, _, err := resolver.Resolve(context.Background(), "new-hash"); !errors.Is(err, ErrAPIKeyResolverClosed) {
		t.Fatalf("new resolve error after close = %v", err)
	}
	close(resolverStore.releaseLoad)
	if err := <-leaderDone; !errors.Is(err, ErrAPIKeyResolverClosed) {
		t.Fatalf("leader error after close = %v", err)
	}
}

func (s *cachedResolverStore) GetKeyByHash(_ context.Context, keyHash string) (*store.APIKey, error) {
	s.getKeyCalls++
	key := s.key
	if s.keys != nil {
		key = s.keys[keyHash]
	}
	if key == nil {
		return nil, nil
	}
	keyCopy := *key
	return &keyCopy, nil
}

func (s *cachedResolverStore) GetUserByID(context.Context, string) (*store.User, error) {
	s.getUserCalls++
	userCopy := *s.user
	return &userCopy, nil
}

func (s *cachedResolverStore) GetTierByID(context.Context, string) (*store.Tier, error) {
	s.getTierCalls++
	tierCopy := *s.tier
	return &tierCopy, nil
}

func TestCachedAPIKeyResolverCachesCompleteAuthenticationSnapshot(t *testing.T) {
	tier := &store.Tier{ID: "tier0-id", Name: "tier0", RPM: 10, SuccessLimit: 1}
	fakeStore := &cachedResolverStore{
		key:  &store.APIKey{ID: "key-id", UserID: "user-id", Enabled: true},
		user: &store.User{ID: "user-id", Enabled: true, TierID: tier.ID},
		tier: tier,
	}
	resolver := NewCachedAPIKeyResolver(fakeStore, time.Hour)
	t.Cleanup(resolver.Close)

	_, firstUser, err := resolver.Resolve(context.Background(), "hashed-key")
	if err != nil {
		t.Fatal(err)
	}
	if firstUser.SuccessLimit != 1 {
		t.Fatalf("first resolve success limit = %d, want 1", firstUser.SuccessLimit)
	}

	firstUser.SuccessLimit = 99
	tier.SuccessLimit = 7
	_, secondUser, err := resolver.Resolve(context.Background(), "hashed-key")
	if err != nil {
		t.Fatal(err)
	}
	if secondUser.SuccessLimit != 1 {
		t.Fatalf("cached authentication snapshot success limit = %d, want 1", secondUser.SuccessLimit)
	}
	if fakeStore.getKeyCalls != 1 {
		t.Fatalf("key lookup should still be cached, got %d lookups", fakeStore.getKeyCalls)
	}
	if fakeStore.getUserCalls != 1 {
		t.Fatalf("user lookup should be cached, got %d lookups", fakeStore.getUserCalls)
	}
	if fakeStore.getTierCalls != 1 {
		t.Fatalf("tier lookup should be cached, got %d lookups", fakeStore.getTierCalls)
	}
}

func TestCachedAPIKeyResolverRefreshesAuthenticationSnapshotAfterInvalidation(t *testing.T) {
	tier := &store.Tier{ID: "tier0-id", Name: "tier0", RPM: 10, SuccessLimit: 1}
	fakeStore := &cachedResolverStore{
		key:  &store.APIKey{ID: "key-id", UserID: "user-id", Enabled: true},
		user: &store.User{ID: "user-id", Enabled: true, TierID: tier.ID},
		tier: tier,
	}
	resolver := NewCachedAPIKeyResolver(fakeStore, time.Hour)
	t.Cleanup(resolver.Close)

	if _, _, err := resolver.Resolve(context.Background(), "hashed-key"); err != nil {
		t.Fatal(err)
	}
	tier.SuccessLimit = 7
	resolver.InvalidateAll()

	_, refreshedUser, err := resolver.Resolve(context.Background(), "hashed-key")
	if err != nil {
		t.Fatal(err)
	}
	if refreshedUser.SuccessLimit != 7 {
		t.Fatalf("refreshed success limit = %d, want 7", refreshedUser.SuccessLimit)
	}
	if fakeStore.getKeyCalls != 2 || fakeStore.getUserCalls != 2 || fakeStore.getTierCalls != 2 {
		t.Fatalf("lookups after invalidation = key:%d user:%d tier:%d, want 2 each", fakeStore.getKeyCalls, fakeStore.getUserCalls, fakeStore.getTierCalls)
	}
}

func TestCachedAPIKeyResolverReclaimsExpiredEntriesOutsideRequestPath(t *testing.T) {
	currentTime := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	tier := &store.Tier{ID: "tier-id", Name: "tier", RPM: 10, SuccessLimit: 10}
	fakeStore := &cachedResolverStore{
		keys: map[string]*store.APIKey{
			"first-hash":  {ID: "first-key", UserID: "user-id", Enabled: true},
			"second-hash": {ID: "second-key", UserID: "user-id", Enabled: true},
			"third-hash":  {ID: "third-key", UserID: "user-id", Enabled: true},
		},
		user: &store.User{ID: "user-id", Enabled: true, TierID: tier.ID},
		tier: tier,
	}
	resolver := NewCachedAPIKeyResolverWithConfig(fakeStore, APIKeyCacheConfig{
		TTL:             10 * time.Second,
		CleanupInterval: time.Hour,
		MaxEntries:      4,
		ShardCount:      1,
	})
	t.Cleanup(resolver.Close)
	resolver.now = func() time.Time { return currentTime }

	if _, _, err := resolver.Resolve(context.Background(), "first-hash"); err != nil {
		t.Fatal(err)
	}
	currentTime = currentTime.Add(11 * time.Second)
	if _, _, err := resolver.Resolve(context.Background(), "second-hash"); err != nil {
		t.Fatal(err)
	}
	shard := &resolver.shards[0]
	shard.mu.Lock()
	_, firstEntryStillPresent := shard.byHash["first-hash"]
	shard.mu.Unlock()
	if !firstEntryStillPresent {
		t.Fatal("resolving another key unexpectedly scanned and removed expired entries")
	}

	resolver.removeExpiredEntries(currentTime)
	shard.mu.Lock()
	_, firstEntryStillPresent = shard.byHash["first-hash"]
	_, secondEntryStillPresent := shard.byHash["second-hash"]
	shard.mu.Unlock()
	if firstEntryStillPresent {
		t.Fatal("expired first entry was not reclaimed")
	}
	if !secondEntryStillPresent {
		t.Fatal("unexpired second entry was removed")
	}
}

func TestCachedAPIKeyResolverEvictsLeastRecentlyUsedEntryAtCapacity(t *testing.T) {
	tier := &store.Tier{ID: "tier-id", Name: "tier", RPM: 10, SuccessLimit: 10}
	fakeStore := &cachedResolverStore{
		keys: map[string]*store.APIKey{
			"first-hash":  {ID: "first-key", UserID: "user-id", Enabled: true},
			"second-hash": {ID: "second-key", UserID: "user-id", Enabled: true},
			"third-hash":  {ID: "third-key", UserID: "user-id", Enabled: true},
		},
		user: &store.User{ID: "user-id", Enabled: true, TierID: tier.ID},
		tier: tier,
	}
	resolver := NewCachedAPIKeyResolverWithConfig(fakeStore, APIKeyCacheConfig{
		TTL:             time.Hour,
		CleanupInterval: time.Hour,
		MaxEntries:      2,
		ShardCount:      1,
	})
	t.Cleanup(resolver.Close)

	for _, keyHash := range []string{"first-hash", "second-hash", "first-hash", "third-hash", "second-hash"} {
		if _, _, err := resolver.Resolve(context.Background(), keyHash); err != nil {
			t.Fatal(err)
		}
	}

	if fakeStore.getKeyCalls != 4 {
		t.Fatalf("key lookups = %d, want 4 after least-recently-used eviction", fakeStore.getKeyCalls)
	}
	shard := &resolver.shards[0]
	shard.mu.Lock()
	entryCount := len(shard.byHash)
	shard.mu.Unlock()
	if entryCount != 2 {
		t.Fatalf("cache entries = %d, want capacity 2", entryCount)
	}
}

func TestCachedAPIKeyResolverBrieflyCachesUnknownHashes(t *testing.T) {
	currentTime := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	fakeStore := &cachedResolverStore{}
	resolver := NewCachedAPIKeyResolverWithConfig(fakeStore, APIKeyCacheConfig{
		TTL:             time.Hour,
		NegativeTTL:     2 * time.Second,
		CleanupInterval: time.Hour,
		MaxEntries:      2,
		ShardCount:      1,
	})
	t.Cleanup(resolver.Close)
	resolver.now = func() time.Time { return currentTime }

	for resolveAttempt := 0; resolveAttempt < 2; resolveAttempt++ {
		key, user, err := resolver.Resolve(context.Background(), "unknown-hash")
		if err != nil {
			t.Fatal(err)
		}
		if key != nil || user != nil {
			t.Fatalf("unknown hash resolved to key=%+v user=%+v", key, user)
		}
	}
	if fakeStore.getKeyCalls != 1 {
		t.Fatalf("repeated unknown hash caused %d lookups, want 1", fakeStore.getKeyCalls)
	}

	currentTime = currentTime.Add(2 * time.Second)
	if _, _, err := resolver.Resolve(context.Background(), "unknown-hash"); err != nil {
		t.Fatal(err)
	}
	if fakeStore.getKeyCalls != 2 {
		t.Fatalf("expired negative entry caused %d lookups, want 2", fakeStore.getKeyCalls)
	}
}
