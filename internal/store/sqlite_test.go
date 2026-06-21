package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
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

func TestCreateAndGetKeyByHash(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	k, raw, err := s.CreateKey(ctx, "test-key", 30)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if raw == "" || k.KeyHash == "" {
		t.Fatal("expected raw key and hash")
	}
	if k.KeyPrefix != raw[:8] {
		t.Fatalf("prefix mismatch: %s", k.KeyPrefix)
	}

	found, err := s.GetKeyByHash(ctx, HashAPIKey(raw))
	if err != nil || found == nil || found.ID != k.ID {
		t.Fatalf("GetKeyByHash: err=%v found=%v", err, found)
	}
}

func TestCreateKeyRequiresName(t *testing.T) {
	s := openTestDB(t)
	_, _, err := s.CreateKey(context.Background(), "  ", 0)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestListUpdateDeleteKey(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	k, _, err := s.CreateKey(ctx, "one", 0)
	if err != nil {
		t.Fatal(err)
	}

	keys, err := s.ListKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListKeys: %v len=%d", err, len(keys))
	}

	name := "renamed"
	rl := 100
	dis := false
	updated, err := s.UpdateKey(ctx, k.ID, KeyUpdates{Name: &name, RateLimit: &rl, Enabled: &dis})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.RateLimit != 100 || updated.Enabled {
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

	k, _, err := s.CreateKey(ctx, "usage", 0)
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