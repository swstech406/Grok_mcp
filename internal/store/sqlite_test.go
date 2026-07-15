package store

import (
	"context"
	"errors"
	"os"
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
	if err := s.ConfigureAPIKeyEncryption("test-api-key-encryption-secret-at-least-32-bytes"); err != nil {
		t.Fatalf("ConfigureAPIKeyEncryption: %v", err)
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

	revealed, err := s.RevealKey(ctx, k.ID)
	if err != nil {
		t.Fatalf("RevealKey: %v", err)
	}
	if revealed != raw {
		t.Fatalf("revealed API key does not match created key")
	}
}

func TestServerSettingsAPIKeyEncryptedAtRestAndReadableAfterReopen(t *testing.T) {
	const encryptionSecret = "test-server-settings-encryption-secret-at-least-32-bytes"
	const cpaAPIKey = "sensitive-cpa-api-key"
	databasePath := filepath.Join(t.TempDir(), "encrypted-settings.db")
	ctx := context.Background()

	sqliteStore, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.ConfigureAPIKeyEncryption(encryptionSecret); err != nil {
		t.Fatal(err)
	}
	userID := testUserID(t, sqliteStore)
	apiKey, rawAPIKey, err := sqliteStore.CreateKey(ctx, userID, "compatibility-key")
	if err != nil {
		t.Fatal(err)
	}

	settings := ServerSettings{
		CPABaseURL:       "http://127.0.0.1:8317",
		CPAAPIKey:        cpaAPIKey,
		UpstreamProtocol: "responses",
		Model:            "grok-4.3",
		TimeoutSeconds:   30,
		RegistrationMode: RegistrationModeFree,
	}
	storedSettings, err := sqliteStore.UpsertServerSettings(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if storedSettings.CPAAPIKey != cpaAPIKey {
		t.Fatalf("returned CPA API key = %q, want original value", storedSettings.CPAAPIKey)
	}

	var plaintext string
	var ciphertext string
	var nonce string
	var encryptionVersion int
	if err := sqliteStore.db.QueryRowContext(ctx, `
		SELECT cpa_api_key, cpa_api_key_ciphertext, cpa_api_key_nonce, cpa_api_key_encryption_version
		FROM server_settings WHERE id = ?`, serverSettingsID,
	).Scan(&plaintext, &ciphertext, &nonce, &encryptionVersion); err != nil {
		t.Fatal(err)
	}
	if plaintext != "" {
		t.Fatalf("plaintext CPA API key remained at rest: %q", plaintext)
	}
	if ciphertext == "" || ciphertext == cpaAPIKey || nonce == "" || encryptionVersion == 0 {
		t.Fatalf("invalid encrypted CPA API key fields: ciphertext=%q nonce=%q version=%d", ciphertext, nonce, encryptionVersion)
	}

	if err := sqliteStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	if err := reopenedStore.ConfigureAPIKeyEncryption(encryptionSecret); err != nil {
		t.Fatal(err)
	}
	reopenedSettings, err := reopenedStore.GetServerSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedSettings == nil || reopenedSettings.CPAAPIKey != cpaAPIKey {
		t.Fatalf("reopened settings = %+v, want decrypted CPA API key", reopenedSettings)
	}
	revealedAPIKey, err := reopenedStore.RevealKey(ctx, apiKey.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revealedAPIKey != rawAPIKey {
		t.Fatal("generalized encryption configuration broke existing API key ciphertext")
	}
}

func TestConfigureAPIKeyEncryptionMigratesLegacyPlaintextServerSettings(t *testing.T) {
	const encryptionSecret = "test-legacy-settings-encryption-secret-at-least-32-bytes"
	const legacyCPAAPIKey = "legacy-plaintext-cpa-key"
	databasePath := filepath.Join(t.TempDir(), "legacy-settings.db")
	ctx := context.Background()

	legacyStore, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := formatTime(time.Now().UTC())
	if _, err := legacyStore.db.ExecContext(ctx, `
		INSERT INTO server_settings (
			id, cpa_base_url, cpa_api_key, upstream_protocol, model, timeout_seconds,
			proxy_url, proxy_enabled, registration_mode, debug, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, '', 0, ?, 0, ?, ?)`,
		serverSettingsID,
		"http://127.0.0.1:8317",
		legacyCPAAPIKey,
		"responses",
		"grok-4.3",
		30,
		string(RegistrationModeFree),
		now,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatal(err)
	}

	migratedStore, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer migratedStore.Close()
	if err := migratedStore.ConfigureAPIKeyEncryption(encryptionSecret); err != nil {
		t.Fatal(err)
	}
	migratedSettings, err := migratedStore.GetServerSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if migratedSettings == nil || migratedSettings.CPAAPIKey != legacyCPAAPIKey {
		t.Fatalf("migrated settings = %+v, want legacy CPA API key", migratedSettings)
	}

	var plaintext string
	var ciphertext string
	var nonce string
	var encryptionVersion int
	if err := migratedStore.db.QueryRowContext(ctx, `
		SELECT cpa_api_key, cpa_api_key_ciphertext, cpa_api_key_nonce, cpa_api_key_encryption_version
		FROM server_settings WHERE id = ?`, serverSettingsID,
	).Scan(&plaintext, &ciphertext, &nonce, &encryptionVersion); err != nil {
		t.Fatal(err)
	}
	if plaintext != "" {
		t.Fatalf("legacy plaintext CPA API key was not cleared: %q", plaintext)
	}
	if ciphertext == "" || ciphertext == legacyCPAAPIKey || nonce == "" || encryptionVersion == 0 {
		t.Fatalf("legacy CPA API key was not migrated to ciphertext: ciphertext=%q nonce=%q version=%d", ciphertext, nonce, encryptionVersion)
	}
}

func TestRotateLegacyAPIKeysPreservesRecordAndInvalidatesOldSecret(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	userID := testUserID(t, s)
	legacyKeyID, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	legacyRawKey := "grok_legacy_key_that_cannot_be_recovered_after_rotation"
	now := formatTime(time.Now().UTC())
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO apikeys (id, user_id, name, key_hash, key_prefix, enabled, created_at, updated_at, total_calls)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, 17)`,
		legacyKeyID, userID, "legacy", keyhash.HashAPIKey(legacyRawKey), legacyRawKey[:8], now, now,
	); err != nil {
		t.Fatal(err)
	}

	rotatedCount, err := s.RotateLegacyAPIKeys(ctx)
	if err != nil {
		t.Fatalf("RotateLegacyAPIKeys: %v", err)
	}
	if rotatedCount != 1 {
		t.Fatalf("rotated key count = %d, want 1", rotatedCount)
	}
	if legacyLookup, err := s.GetKeyByHash(ctx, keyhash.HashAPIKey(legacyRawKey)); err != nil || legacyLookup != nil {
		t.Fatalf("legacy key must stop authenticating, lookup=%v err=%v", legacyLookup, err)
	}

	rotatedKey, err := s.GetKeyByID(ctx, legacyKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedKey.Name != "legacy" || rotatedKey.TotalCalls != 17 || !rotatedKey.Enabled {
		t.Fatalf("rotation must preserve metadata and usage, got %+v", rotatedKey)
	}
	replacementRawKey, err := s.RevealKey(ctx, legacyKeyID)
	if err != nil {
		t.Fatalf("RevealKey after rotation: %v", err)
	}
	if replacementRawKey == legacyRawKey || replacementRawKey == "" {
		t.Fatalf("rotation must generate a distinct replacement key")
	}
	if replacementLookup, err := s.GetKeyByHash(ctx, keyhash.HashAPIKey(replacementRawKey)); err != nil || replacementLookup == nil {
		t.Fatalf("replacement key must authenticate, lookup=%v err=%v", replacementLookup, err)
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

func TestUsageDebugBodiesPersistAndLoadInBoundedChunks(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	userID := testUserID(t, store)
	key, _, err := store.CreateKey(ctx, userID, "debug-capture")
	if err != nil {
		t.Fatal(err)
	}

	requestBody := strings.Repeat("request-body-segment|", 120000)
	responseBody := strings.Repeat("response-body-segment|", 550000)
	requestPath := filepath.Join(t.TempDir(), "request.body")
	responsePath := filepath.Join(t.TempDir(), "response.body")
	if err := os.WriteFile(requestPath, []byte(requestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(responsePath, []byte(responseBody), 0o600); err != nil {
		t.Fatal(err)
	}

	debugJSON := `{"version":2,"request":{"body_storage":"sqlite_chunks"},"response":{"body_storage":"sqlite_chunks"}}`
	recordTimestamp := time.Now().UTC()
	if err := store.RecordUsage(ctx, UsageRecord{
		KeyID:                 key.ID,
		ToolName:              "grok_web_search",
		Timestamp:             recordTimestamp,
		DurationMs:            42,
		Success:               true,
		DebugJSON:             debugJSON,
		DebugRequestBodyPath:  requestPath,
		DebugResponseBodyPath: responsePath,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.GetUsageStats(ctx, key.ID, recordTimestamp.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Records) != 1 {
		t.Fatalf("usage records = %d, want 1", len(stats.Records))
	}
	persistedRecord := stats.Records[0]
	if persistedRecord.DebugJSON != debugJSON {
		t.Fatalf("debug metadata changed: got %q want %q", persistedRecord.DebugJSON, debugJSON)
	}
	if persistedRecord.DebugRequestBody != "" || persistedRecord.DebugResponseBody != "" {
		t.Fatal("usage list must not load complete debug bodies")
	}
	if !persistedRecord.HasDebugRequestBody || persistedRecord.DebugRequestBytes != int64(len(requestBody)) {
		t.Fatalf("request body summary = available:%v bytes:%d, want true/%d", persistedRecord.HasDebugRequestBody, persistedRecord.DebugRequestBytes, len(requestBody))
	}
	if !persistedRecord.HasDebugResponseBody || persistedRecord.DebugResponseBytes != int64(len(responseBody)) {
		t.Fatalf("response body summary = available:%v bytes:%d, want true/%d", persistedRecord.HasDebugResponseBody, persistedRecord.DebugResponseBytes, len(responseBody))
	}
	if persistedRecord.DebugRequestBodyPath != "" || persistedRecord.DebugResponseBodyPath != "" {
		t.Fatalf("temporary paths must not be returned from stats: %+v", persistedRecord)
	}

	detailRecord, err := store.GetUsageRecordDetail(ctx, persistedRecord.ID, UsageRecordScope{UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if detailRecord.DebugRequestBody != requestBody {
		t.Fatalf("detail request body length = %d, want %d", len(detailRecord.DebugRequestBody), len(requestBody))
	}
	if detailRecord.DebugResponseBody != responseBody {
		t.Fatalf("detail response body length = %d, want %d", len(detailRecord.DebugResponseBody), len(responseBody))
	}

	var chunkCount int
	var maximumChunkBytes int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(length(body_data)), 0) FROM usage_log_debug_body_chunks WHERE usage_id = ?`,
		persistedRecord.ID,
	).Scan(&chunkCount, &maximumChunkBytes); err != nil {
		t.Fatal(err)
	}
	if chunkCount < 3 {
		t.Fatalf("chunk count = %d, want multiple bounded chunks", chunkCount)
	}
	if maximumChunkBytes > usageDebugBodyChunkSize {
		t.Fatalf("maximum chunk size = %d, cap = %d", maximumChunkBytes, usageDebugBodyChunkSize)
	}
}

func TestGetUsageRecordDetailEnforcesUserScope(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	owner, err := store.CreateUser(ctx, "usage-owner", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := store.CreateUser(ctx, "usage-other", "hash", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := store.CreateKey(ctx, owner.ID, "owned-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsage(ctx, UsageRecord{
		KeyID: key.ID, ToolName: "grok_x_search", Timestamp: time.Now().UTC(), DebugJSON: `{"version":2}`,
	}); err != nil {
		t.Fatal(err)
	}

	var usageID int64
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM usage_log WHERE key_id = ?`, key.ID).Scan(&usageID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetUsageRecordDetail(ctx, usageID, UsageRecordScope{UserID: otherUser.ID}); !errors.Is(err, ErrUsageRecordNotFound) {
		t.Fatalf("other user detail error = %v, want ErrUsageRecordNotFound", err)
	}
	ownerRecord, err := store.GetUsageRecordDetail(ctx, usageID, UsageRecordScope{UserID: owner.ID})
	if err != nil || ownerRecord.ID != usageID {
		t.Fatalf("owner detail = %+v, err = %v", ownerRecord, err)
	}
	adminRecord, err := store.GetUsageRecordDetail(ctx, usageID, UsageRecordScope{IncludeAllUsers: true})
	if err != nil || adminRecord.ID != usageID {
		t.Fatalf("admin detail = %+v, err = %v", adminRecord, err)
	}
}

func TestUsageDebugBodyPersistenceRollsBackTransactionOnSpoolFailure(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	key, _, err := store.CreateKey(ctx, testUserID(t, store), "debug-rollback")
	if err != nil {
		t.Fatal(err)
	}

	requestPath := filepath.Join(t.TempDir(), "request.body")
	if err := os.WriteFile(requestPath, []byte("request persisted before response failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	missingResponsePath := filepath.Join(t.TempDir(), "missing-response.body")
	err = store.RecordUsage(ctx, UsageRecord{
		KeyID:                 key.ID,
		ToolName:              "grok_web_search",
		Timestamp:             time.Now().UTC(),
		DebugRequestBodyPath:  requestPath,
		DebugResponseBodyPath: missingResponsePath,
	})
	if err == nil {
		t.Fatal("expected missing response spool to fail persistence")
	}

	var usageCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_log WHERE key_id = ?`, key.ID).Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	if usageCount != 0 {
		t.Fatalf("usage rows after rollback = %d, want 0", usageCount)
	}
	var bodyChunkCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_log_debug_body_chunks`).Scan(&bodyChunkCount); err != nil {
		t.Fatal(err)
	}
	if bodyChunkCount != 0 {
		t.Fatalf("body chunks after rollback = %d, want 0", bodyChunkCount)
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
