package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/keyhash"
)

func openTestDB(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testUserID(t *testing.T, s *SQLiteStore) string {
	t.Helper()
	u, err := s.CreateUser(context.Background(), "keyowner", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestCreateAndGetKeyByHash(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	uid := testUserID(t, s)

	k, raw, err := s.CreateKey(ctx, uid, "test-key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if raw == "" || k.KeyHash == "" {
		t.Fatal("expected raw key and hash")
	}
	if k.KeyPrefix != raw[:8] {
		t.Fatalf("prefix mismatch: %s", k.KeyPrefix)
	}

	found, err := s.GetKeyByHash(ctx, keyhash.HashAPIKey(raw))
	if err != nil || found == nil || found.ID != k.ID {
		t.Fatalf("GetKeyByHash: err=%v found=%v", err, found)
	}
}

func TestCreateKeyRequiresName(t *testing.T) {
	s := openTestDB(t)
	uid := testUserID(t, s)
	_, _, err := s.CreateKey(context.Background(), uid, "  ")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestListUpdateDeleteKey(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	uid := testUserID(t, s)

	k, _, err := s.CreateKey(ctx, uid, "one")
	if err != nil {
		t.Fatal(err)
	}

	keys, err := s.ListKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListKeys: %v len=%d", err, len(keys))
	}

	name := "renamed"
	dis := false
	updated, err := s.UpdateKey(ctx, k.ID, KeyUpdates{Name: &name, Enabled: &dis})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.Enabled {
		t.Fatalf("unexpected update: %+v", updated)
	}

	if err := s.DeleteKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	keys, _ = s.ListKeys(ctx)
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys after delete")
	}
}

func TestUsageStats(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	k, _, err := s.CreateKey(ctx, testUserID(t, s), "usage")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if err := s.RecordUsage(ctx, UsageRecord{
			KeyID: k.ID, ToolName: "grok_web_search", Timestamp: now, DurationMs: 10,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.TouchKeyUsage(ctx, k.ID); err != nil {
		t.Fatal(err)
	}

	stats, err := s.GetUsageStats(ctx, k.ID, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalCalls != 3 || stats.ByTool["grok_web_search"] != 3 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	g, err := s.GetGlobalStats(ctx, now.Add(-time.Hour))
	if err != nil || g.TotalCalls != 3 {
		t.Fatalf("global stats: %+v err=%v", g, err)
	}
}

func TestUsageStatsSinceFilterAndUserScope(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	firstUserID := testUserID(t, s)
	secondUser, err := s.CreateUser(ctx, "second-keyowner", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	firstKey, _, err := s.CreateKey(ctx, firstUserID, "first")
	if err != nil {
		t.Fatal(err)
	}
	secondKey, _, err := s.CreateKey(ctx, secondUser.ID, "second")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	records := []UsageRecord{
		{KeyID: firstKey.ID, ToolName: "grok_web_search", Timestamp: now.Add(-2 * time.Hour), DurationMs: 10, Success: true},
		{KeyID: firstKey.ID, ToolName: "grok_web_search", Timestamp: now, DurationMs: 11, Success: true},
		{KeyID: firstKey.ID, ToolName: "grok_x_search", Timestamp: now, DurationMs: 12, Success: false},
		{KeyID: secondKey.ID, ToolName: "grok_web_search", Timestamp: now, DurationMs: 13, Success: true},
	}
	for _, record := range records {
		if err := s.RecordUsage(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := s.GetUsageStats(ctx, firstKey.ID, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalCalls != 2 || stats.SuccessCalls != 1 {
		t.Fatalf("expected recent key stats total=2 success=1, got %+v", stats)
	}
	if stats.ByTool["grok_web_search"] != 1 || stats.ByTool["grok_x_search"] != 1 {
		t.Fatalf("unexpected key stats by tool: %+v", stats.ByTool)
	}

	userStats, err := s.GetUserUsageStats(ctx, firstUserID, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if userStats.TotalCalls != 2 {
		t.Fatalf("expected first user scope to exclude second user's key, got %+v", userStats)
	}
}

func TestUsageStatsAggregatesTrafficAndRPMBeyondRecordLimit(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	userID := testUserID(t, s)
	key, _, err := s.CreateKey(ctx, userID, "high-volume")
	if err != nil {
		t.Fatal(err)
	}

	const recordCount = 650
	recordTimestamp := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Second)
	for recordIndex := 0; recordIndex < recordCount; recordIndex++ {
		if err := s.RecordUsage(ctx, UsageRecord{
			KeyID:      key.ID,
			ToolName:   "grok_web_search",
			Timestamp:  recordTimestamp,
			DurationMs: int64(recordIndex),
			Success:    true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := s.GetUserUsageStats(ctx, userID, recordTimestamp.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalCalls != recordCount || stats.SuccessCalls != recordCount {
		t.Fatalf("expected complete aggregate counts of %d, got %+v", recordCount, stats)
	}
	if len(stats.Records) != 500 {
		t.Fatalf("expected recent activity to remain limited to 500 records, got %d", len(stats.Records))
	}
	if stats.CurrentRPM != recordCount {
		t.Fatalf("expected current RPM to count all %d records, got %d", recordCount, stats.CurrentRPM)
	}
	if len(stats.TrafficBuckets) != usageTrafficBucketCount {
		t.Fatalf("expected %d traffic buckets, got %d", usageTrafficBucketCount, len(stats.TrafficBuckets))
	}

	var bucketCallCount int64
	for _, bucket := range stats.TrafficBuckets {
		bucketCallCount += bucket.Calls
	}
	if bucketCallCount != recordCount {
		t.Fatalf("expected traffic buckets to count all %d records, got %d", recordCount, bucketCallCount)
	}
}

func TestAsyncUsageWriterCloseFlushesUsageAndTouch(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	key, _, err := s.CreateKey(ctx, testUserID(t, s), "async")
	if err != nil {
		t.Fatal(err)
	}

	writer := NewAsyncUsageWriter(s, 8)
	now := time.Now().UTC()
	writer.Enqueue(UsageRecord{KeyID: key.ID, TouchKey: true})
	writer.Enqueue(UsageRecord{KeyID: key.ID, ToolName: "grok_web_search", Timestamp: now, DurationMs: 21, Success: true})
	writer.Enqueue(UsageRecord{KeyID: key.ID, ToolName: "grok_x_search", Timestamp: now, DurationMs: 22, Success: false})
	writer.Close()

	updatedKey, err := s.GetKeyByID(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedKey.TotalCalls != 1 || updatedKey.LastUsedAt == nil {
		t.Fatalf("expected touch record to update key usage, got %+v", updatedKey)
	}

	stats, err := s.GetUsageStats(ctx, key.ID, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalCalls != 2 || stats.SuccessCalls != 1 {
		t.Fatalf("expected async writer to flush two usage rows with one success, got %+v", stats)
	}
	if stats.ByTool["grok_web_search"] != 1 || stats.ByTool["grok_x_search"] != 1 {
		t.Fatalf("unexpected async usage by tool: %+v", stats.ByTool)
	}
}

func TestUpdateUserTokenVersionSemantics(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "token-version-user", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	initialTokenVersion := user.TokenVersion

	tier, err := s.GetTierByName(ctx, "tier1")
	if err != nil || tier == nil {
		t.Fatalf("tier1 should be seeded by migration: %v", err)
	}
	updated, err := s.UpdateUser(ctx, user.ID, UserUpdates{TierID: &tier.ID})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TokenVersion != initialTokenVersion {
		t.Fatalf("tier change must not revoke JWTs, token_version=%d want %d", updated.TokenVersion, initialTokenVersion)
	}

	unchangedEnabled := updated.Enabled
	unchangedRole := updated.Role
	updated, err = s.UpdateUser(ctx, user.ID, UserUpdates{Enabled: &unchangedEnabled, Role: &unchangedRole})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TokenVersion != initialTokenVersion {
		t.Fatalf("unchanged enabled/role must not revoke JWTs, token_version=%d want %d", updated.TokenVersion, initialTokenVersion)
	}

	disabled := false
	updated, err = s.UpdateUser(ctx, user.ID, UserUpdates{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TokenVersion != initialTokenVersion+1 {
		t.Fatalf("enabled change must bump token_version, got %d", updated.TokenVersion)
	}

	role := RoleAdmin
	updated, err = s.UpdateUser(ctx, user.ID, UserUpdates{Role: &role})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TokenVersion != initialTokenVersion+2 {
		t.Fatalf("role change must bump token_version, got %d", updated.TokenVersion)
	}

	revokeTokens := true
	updated, err = s.UpdateUser(ctx, user.ID, UserUpdates{RevokeTokens: &revokeTokens})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TokenVersion != initialTokenVersion+3 {
		t.Fatalf("explicit revocation must bump token_version, got %d", updated.TokenVersion)
	}
}

func TestTierLifecycleValidationAndInUseProtection(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	tier, err := s.CreateTier(ctx, "paid", 7, 70, 700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTier(ctx, "PAID", 8, 80, 800); !errors.Is(err, ErrTierNameTaken) {
		t.Fatalf("expected case-insensitive duplicate tier name error, got %v", err)
	}

	newName := "pro"
	newRPM := 120
	newSuccessLimit := 1200
	updatedTier, err := s.UpdateTier(ctx, tier.ID, TierUpdates{Name: &newName, RPM: &newRPM, SuccessLimit: &newSuccessLimit})
	if err != nil {
		t.Fatal(err)
	}
	if updatedTier.Name != newName || updatedTier.RPM != newRPM || updatedTier.SuccessLimit != newSuccessLimit {
		t.Fatalf("unexpected updated tier: %+v", updatedTier)
	}

	user, err := s.CreateUser(ctx, "tier-user", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	inUseTier, err := s.GetTierByName(ctx, "tier1")
	if err != nil || inUseTier == nil {
		t.Fatalf("tier1 should be seeded by migration: %v", err)
	}
	if _, err := s.UpdateUser(ctx, user.ID, UserUpdates{TierID: &inUseTier.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTier(ctx, inUseTier.ID); !errors.Is(err, ErrTierInUse) {
		t.Fatalf("expected in-use tier delete to fail, got %v", err)
	}

	unusedTier, err := s.CreateTier(ctx, "unused", 99, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTier(ctx, unusedTier.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTierByID(ctx, unusedTier.ID); !errors.Is(err, ErrTierNotFound) {
		t.Fatalf("expected deleted tier to be missing, got %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	s1, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
}

func TestOpenSQLiteAppliesAllEmbeddedMigrations(t *testing.T) {
	s := openTestDB(t)

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var expectedMigrationCount int
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			expectedMigrationCount++
		}
	}

	var appliedMigrationCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&appliedMigrationCount); err != nil {
		t.Fatal(err)
	}
	if appliedMigrationCount != expectedMigrationCount {
		t.Fatalf("applied migrations = %d, want %d", appliedMigrationCount, expectedMigrationCount)
	}
}
