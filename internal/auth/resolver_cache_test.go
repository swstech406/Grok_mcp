package auth

import (
	"context"
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
