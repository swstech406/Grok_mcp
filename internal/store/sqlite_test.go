package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/keyhash"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/settings"
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

func readQueryPlanDetails(t *testing.T, database *sql.DB, query string, arguments ...any) []string {
	t.Helper()

	rows, err := database.QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+query, arguments...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()

	var planDetails []string
	for rows.Next() {
		var selectID int
		var parentID int
		var unusedValue int
		var detail string
		if err := rows.Scan(&selectID, &parentID, &unusedValue, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		planDetails = append(planDetails, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	return planDetails
}

func TestSQLiteCoreUsageAndOwnershipQueriesUseIndexes(t *testing.T) {
	sqliteStore := openTestDB(t)
	sinceTimestamp := formatTime(time.Now().UTC().Add(-time.Hour))

	testCases := []struct {
		name            string
		query           string
		arguments       []any
		expectedIndexes []string
	}{
		{
			name:            "usage by key and timestamp",
			query:           buildUsageStatsAggregateQuery(usageStatsWhere[usageStatsByKey]),
			arguments:       []any{"key-id", sinceTimestamp},
			expectedIndexes: []string{"idx_usage_log_key_id_timestamp"},
		},
		{
			name:            "usage by user ownership and timestamp",
			query:           buildUsageStatsAggregateQuery(usageStatsWhere[usageStatsByUser]),
			arguments:       []any{"user-id", sinceTimestamp},
			expectedIndexes: []string{"idx_usage_log_key_id_timestamp", "idx_apikeys_user_id"},
		},
		{
			name:            "API keys by owner",
			query:           listKeysByUserQuery,
			arguments:       []any{"user-id"},
			expectedIndexes: []string{"idx_apikeys_user_id"},
		},
		{
			name:            "users by tier",
			query:           countUsersByTierQuery,
			arguments:       []any{"tier-id"},
			expectedIndexes: []string{"idx_users_tier_id"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			planDetails := readQueryPlanDetails(t, sqliteStore.readDB, testCase.query, testCase.arguments...)
			joinedPlan := strings.Join(planDetails, "\n")
			for _, expectedIndex := range testCase.expectedIndexes {
				if !strings.Contains(joinedPlan, expectedIndex) {
					t.Fatalf("query plan did not use %s:\n%s", expectedIndex, joinedPlan)
				}
			}
		})
	}
}

func TestSQLiteReadPoolIsBoundedAndQueryOnly(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()

	if maximumOpenConnections := sqliteStore.readDB.Stats().MaxOpenConnections; maximumOpenConnections != sqliteReadPoolSize {
		t.Fatalf("read pool MaxOpenConnections = %d, want %d", maximumOpenConnections, sqliteReadPoolSize)
	}

	var queryOnly int
	if err := sqliteStore.readDB.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil {
		t.Fatalf("read PRAGMA query_only: %v", err)
	}
	if queryOnly != 1 {
		t.Fatalf("PRAGMA query_only = %d, want 1", queryOnly)
	}

	if _, err := sqliteStore.readDB.ExecContext(ctx, `UPDATE users SET username = username`); err == nil {
		t.Fatal("expected the read pool to reject writes")
	}
}

func TestSQLiteInMemoryDatabaseKeepsSharedConnection(t *testing.T) {
	sqliteStore, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	if sqliteStore.readDB != sqliteStore.db {
		t.Fatal("in-memory SQLite must reuse the writer pool so reads see the same database")
	}
	if _, err := sqliteStore.CountUsers(context.Background()); err != nil {
		t.Fatalf("query in-memory database: %v", err)
	}
}

func TestSQLiteCreatesSeparateRestrictedDebugDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "split-storage.db")
	sqliteStore, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	debugPath := debugDatabasePath(databasePath)
	debugInfo, err := os.Stat(debugPath)
	if err != nil {
		t.Fatalf("stat debug database: %v", err)
	}
	if permissions := debugInfo.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("debug database permissions = %#o, want 0600", permissions)
	}

	var debugTableCount int
	if err := sqliteStore.debugDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'usage_debug'`).Scan(&debugTableCount); err != nil {
		t.Fatalf("query debug schema: %v", err)
	}
	if debugTableCount != 1 {
		t.Fatalf("debug table count = %d, want 1", debugTableCount)
	}

	var mainDebugTableCount int
	if err := sqliteStore.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'usage_debug'`).Scan(&mainDebugTableCount); err != nil {
		t.Fatalf("query main schema: %v", err)
	}
	if mainDebugTableCount != 0 {
		t.Fatalf("main database debug table count = %d, want 0", mainDebugTableCount)
	}
}

func TestSQLiteStrictSchemaExcludesRetiredStorage(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()

	for _, retiredTableName := range []string{"usage_log_debug_body_chunks"} {
		var tableCount int
		if err := sqliteStore.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			retiredTableName,
		).Scan(&tableCount); err != nil {
			t.Fatal(err)
		}
		if tableCount != 0 {
			t.Fatalf("retired table %q still exists", retiredTableName)
		}
	}

	retiredColumns := map[string]string{
		"usage_log":       "debug_json",
		"server_settings": "cpa_api_key",
	}
	for tableName, retiredColumnName := range retiredColumns {
		rows, err := sqliteStore.db.QueryContext(ctx, `PRAGMA table_info(`+tableName+`)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var columnID int
			var columnName string
			var columnType string
			var notNull int
			var defaultValue any
			var primaryKey int
			if err := rows.Scan(&columnID, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if columnName == retiredColumnName {
				_ = rows.Close()
				t.Fatalf("retired column %s.%s still exists", tableName, retiredColumnName)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}

	var tierIDNotNull int
	rows, err := sqliteStore.db.QueryContext(ctx, `PRAGMA table_info(users)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var columnID int
		var columnName string
		var columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&columnID, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if columnName == "tier_id" {
			tierIDNotNull = notNull
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if tierIDNotNull != 1 {
		t.Fatalf("users.tier_id NOT NULL = %d, want 1", tierIDNotNull)
	}

	userID := testUserID(t, sqliteStore)
	if _, err := sqliteStore.db.ExecContext(ctx,
		`INSERT INTO apikeys (
			id, user_id, name, key_hash, key_prefix, key_ciphertext, key_nonce,
			key_encryption_version, enabled, created_at, updated_at
		) VALUES ('invalid-key', ?, 'invalid', 'invalid-hash', 'invalid', '', '', 0, 1, ?, ?)`,
		userID,
		formatTime(time.Now().UTC()),
		formatTime(time.Now().UTC()),
	); err == nil {
		t.Fatal("expected strict API key encryption constraints to reject empty ciphertext")
	}

	user, err := sqliteStore.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteStore.db.ExecContext(ctx, `DELETE FROM tiers WHERE id = ?`, user.TierID); err == nil {
		t.Fatal("expected database foreign key to restrict deleting an assigned tier")
	}
}

func TestSQLiteWriterDoesNotWaitForHeldReader(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)

	readTransaction, err := sqliteStore.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin read transaction: %v", err)
	}
	defer readTransaction.Rollback()

	var username string
	if err := readTransaction.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, userID).Scan(&username); err != nil {
		t.Fatalf("establish reader snapshot: %v", err)
	}

	writeContext, cancelWrite := context.WithTimeout(ctx, time.Second)
	defer cancelWrite()
	if _, err := sqliteStore.db.ExecContext(writeContext,
		`UPDATE users SET success_calls = success_calls + 1 WHERE id = ?`, userID,
	); err != nil {
		t.Fatalf("writer was blocked by held reader: %v", err)
	}
}

func BenchmarkSQLiteMixedAuthenticationReadsAndWrites(b *testing.B) {
	databasePath := filepath.Join(b.TempDir(), "benchmark.db")
	sqliteStore, err := OpenSQLite(databasePath)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.ConfigureAPIKeyEncryption("benchmark-api-key-encryption-secret-at-least-32-bytes"); err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	user, err := sqliteStore.CreateUser(ctx, "benchmark-user", "hash", RoleUser)
	if err != nil {
		b.Fatal(err)
	}
	apiKey, rawAPIKey, err := sqliteStore.CreateKey(ctx, user.ID, "benchmark-key", 20)
	if err != nil {
		b.Fatal(err)
	}
	apiKeyHash := keyhash.HashAPIKey(rawAPIKey)

	benchmarks := []struct {
		name         string
		readDatabase *sql.DB
	}{
		{name: "serialized", readDatabase: sqliteStore.db},
		{name: "split-read-pool", readDatabase: sqliteStore.readDB},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			var operationCounter uint64
			var firstError error
			var firstErrorOnce sync.Once

			b.ResetTimer()
			b.RunParallel(func(parallelBenchmark *testing.PB) {
				for parallelBenchmark.Next() {
					operationNumber := atomic.AddUint64(&operationCounter, 1)
					if operationNumber%10 == 0 {
						if err := sqliteStore.TouchKeyUsage(ctx, apiKey.ID); err != nil {
							firstErrorOnce.Do(func() { firstError = err })
							return
						}
						continue
					}

					foundAPIKey, err := scanAPIKey(benchmark.readDatabase.QueryRowContext(ctx,
						`SELECT `+keyColumns+` FROM apikeys WHERE key_hash = ?`, apiKeyHash,
					))
					if err != nil {
						firstErrorOnce.Do(func() { firstError = err })
						return
					}
					if foundAPIKey.ID != apiKey.ID {
						firstErrorOnce.Do(func() {
							firstError = fmt.Errorf("read API key ID %q, want %q", foundAPIKey.ID, apiKey.ID)
						})
						return
					}
				}
			})
			if firstError != nil {
				b.Fatal(firstError)
			}
		})
	}
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

	k, raw, err := s.CreateKey(ctx, uid, "test-key", 20)
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

func TestRecordUsageUpdatesKeyAccountingWithoutRegressingLastUsedAt(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)
	apiKey, _, err := sqliteStore.CreateKey(ctx, userID, "usage-key", 20)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	newerTimestamp := time.Date(2026, time.July, 15, 12, 30, 45, 0, time.UTC)
	olderTimestamp := newerTimestamp.Add(-2 * time.Hour)
	for _, usageTimestamp := range []time.Time{newerTimestamp, olderTimestamp} {
		if err := sqliteStore.RecordUsage(ctx, UsageRecord{
			KeyID:      apiKey.ID,
			ToolName:   "grok_web_search",
			Timestamp:  usageTimestamp,
			DurationMs: 25,
			Success:    true,
		}); err != nil {
			t.Fatalf("RecordUsage(%s): %v", usageTimestamp, err)
		}
	}

	updatedKey, err := sqliteStore.GetKeyByID(ctx, apiKey.ID)
	if err != nil {
		t.Fatalf("GetKeyByID: %v", err)
	}
	if updatedKey.TotalCalls != 2 {
		t.Fatalf("total calls = %d, want 2", updatedKey.TotalCalls)
	}
	if updatedKey.LastUsedAt == nil {
		t.Fatal("last used timestamp was not recorded")
	}
	if !updatedKey.LastUsedAt.Equal(newerTimestamp) {
		t.Fatalf("last used timestamp = %s, want %s", updatedKey.LastUsedAt, newerTimestamp)
	}

	var usageCount int
	if err := sqliteStore.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_log WHERE key_id = ?`, apiKey.ID,
	).Scan(&usageCount); err != nil {
		t.Fatalf("count usage records: %v", err)
	}
	if usageCount != 2 {
		t.Fatalf("usage records = %d, want 2", usageCount)
	}
}

func TestRecordUsageRollsBackLogWhenKeyAccountingFails(t *testing.T) {
	sqliteStore := openTestDB(t)
	ctx := context.Background()
	userID := testUserID(t, sqliteStore)
	apiKey, _, err := sqliteStore.CreateKey(ctx, userID, "rollback-key", 20)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	if _, err := sqliteStore.db.ExecContext(ctx, `
		CREATE TRIGGER reject_usage_accounting
		BEFORE UPDATE OF total_calls ON apikeys
		BEGIN
			SELECT RAISE(ABORT, 'usage accounting rejected');
		END`); err != nil {
		t.Fatalf("create accounting rejection trigger: %v", err)
	}

	err = sqliteStore.RecordUsage(ctx, UsageRecord{
		KeyID:      apiKey.ID,
		ToolName:   "grok_web_search",
		Timestamp:  time.Now().UTC(),
		DurationMs: 10,
		Success:    true,
	})
	if err == nil {
		t.Fatal("expected RecordUsage to fail when key accounting is rejected")
	}

	var usageCount int
	if err := sqliteStore.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_log WHERE key_id = ?`, apiKey.ID,
	).Scan(&usageCount); err != nil {
		t.Fatalf("count usage records: %v", err)
	}
	if usageCount != 0 {
		t.Fatalf("usage records after rollback = %d, want 0", usageCount)
	}

	updatedKey, err := sqliteStore.GetKeyByID(ctx, apiKey.ID)
	if err != nil {
		t.Fatalf("GetKeyByID: %v", err)
	}
	if updatedKey.TotalCalls != 0 || updatedKey.LastUsedAt != nil {
		t.Fatalf("key accounting changed after rollback: total_calls=%d last_used_at=%v", updatedKey.TotalCalls, updatedKey.LastUsedAt)
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
	apiKey, rawAPIKey, err := sqliteStore.CreateKey(ctx, userID, "compatibility-key", 20)
	if err != nil {
		t.Fatal(err)
	}

	serverSettings := ServerSettings{
		Runtime: settings.Runtime{
			CPABaseURL:                 "http://127.0.0.1:8317",
			CPAAPIKey:                  cpaAPIKey,
			UpstreamProtocol:           "responses",
			Model:                      "grok-4.3",
			TimeoutSeconds:             30,
			MCPGlobalSearchConcurrency: 12,
			MCPUserSearchConcurrency:   3,
			RegistrationMode:           RegistrationModeFree,
		},
	}
	storedSettings, err := sqliteStore.UpsertServerSettings(ctx, serverSettings)
	if err != nil {
		t.Fatal(err)
	}
	if storedSettings.Revision != 1 {
		t.Fatalf("initial settings revision = %d, want 1", storedSettings.Revision)
	}
	if storedSettings.CPAAPIKey != cpaAPIKey {
		t.Fatalf("returned CPA API key = %q, want original value", storedSettings.CPAAPIKey)
	}
	serverSettings.Model = "grok-4.4"
	updatedSettings, err := sqliteStore.UpsertServerSettings(ctx, serverSettings)
	if err != nil {
		t.Fatal(err)
	}
	if updatedSettings.Revision != 2 {
		t.Fatalf("updated settings revision = %d, want 2", updatedSettings.Revision)
	}
	if updatedSettings.Model != "grok-4.4" {
		t.Fatalf("updated settings model = %q, want grok-4.4", updatedSettings.Model)
	}

	var ciphertext string
	var nonce string
	var encryptionVersion int
	if err := sqliteStore.db.QueryRowContext(ctx, `
		SELECT cpa_api_key_ciphertext, cpa_api_key_nonce, cpa_api_key_encryption_version
		FROM server_settings WHERE id = ?`, serverSettingsID,
	).Scan(&ciphertext, &nonce, &encryptionVersion); err != nil {
		t.Fatal(err)
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
	if reopenedSettings.Revision != 2 {
		t.Fatalf("reopened settings revision = %d, want 2", reopenedSettings.Revision)
	}
	if reopenedSettings.Model != "grok-4.4" {
		t.Fatalf("reopened settings model = %q, want grok-4.4", reopenedSettings.Model)
	}
	if reopenedSettings.MCPGlobalSearchConcurrency != 12 || reopenedSettings.MCPUserSearchConcurrency != 3 {
		t.Fatalf("reopened search concurrency settings = %+v", reopenedSettings)
	}
	revealedAPIKey, err := reopenedStore.RevealKey(ctx, apiKey.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revealedAPIKey != rawAPIKey {
		t.Fatal("generalized encryption configuration broke existing API key ciphertext")
	}
}

func TestServerSettingsRevisionMigrationBackfillsExistingRow(t *testing.T) {
	const encryptionSecret = "test-server-settings-migration-secret-at-least-32-bytes"
	databasePath := filepath.Join(t.TempDir(), "settings-before-revision.db")
	ctx := context.Background()

	currentStore, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := currentStore.ConfigureAPIKeyEncryption(encryptionSecret); err != nil {
		t.Fatal(err)
	}
	originalSettings := ServerSettings{Runtime: settings.Runtime{
		CPABaseURL:                 "http://127.0.0.1:8317",
		CPAAPIKey:                  "migration-api-key",
		UpstreamProtocol:           "responses",
		Model:                      "grok-4.3",
		TimeoutSeconds:             30,
		MCPGlobalSearchConcurrency: 12,
		MCPUserSearchConcurrency:   3,
		RegistrationMode:           RegistrationModeFree,
	}}
	if _, err := currentStore.UpsertServerSettings(ctx, originalSettings); err != nil {
		t.Fatal(err)
	}
	if err := currentStore.Close(); err != nil {
		t.Fatal(err)
	}

	rawDatabase, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDatabase.Exec(`ALTER TABLE server_settings DROP COLUMN revision`); err != nil {
		_ = rawDatabase.Close()
		t.Fatalf("create pre-revision settings fixture: %v", err)
	}
	if _, err := rawDatabase.Exec(`DELETE FROM schema_migrations WHERE version = '008_server_settings_revision'`); err != nil {
		_ = rawDatabase.Close()
		t.Fatal(err)
	}
	if err := rawDatabase.Close(); err != nil {
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
	if migratedSettings.Revision != 1 {
		t.Fatalf("migrated settings revision = %d, want 1", migratedSettings.Revision)
	}
	if migratedSettings.CPAAPIKey != originalSettings.CPAAPIKey || migratedSettings.Model != originalSettings.Model {
		t.Fatalf("migrated settings did not preserve values: %+v", migratedSettings)
	}

	migratedSettings.Model = "grok-4.4"
	updatedSettings, err := migratedStore.UpsertServerSettings(ctx, *migratedSettings)
	if err != nil {
		t.Fatal(err)
	}
	if updatedSettings.Revision != 2 || updatedSettings.Model != "grok-4.4" {
		t.Fatalf("updated migrated settings = %+v", updatedSettings)
	}
}

func TestInviteCodePlaintextMigrationClearsLegacyValueAndPreservesRedemption(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "invite-plaintext-migration.db")
	requestContext := context.Background()

	legacyStore, err := OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := legacyStore.CreateUser(requestContext, "legacy-invite-admin", "hash", RoleAdmin)
	if err != nil {
		_ = legacyStore.Close()
		t.Fatal(err)
	}
	inviteCode, rawInviteCode, err := legacyStore.CreateInviteCode(requestContext, creator.ID, 3)
	if err != nil {
		_ = legacyStore.Close()
		t.Fatal(err)
	}
	if _, err := legacyStore.RegisterUserWithInviteCode(requestContext, "legacy-first-redemption", "hash", rawInviteCode); err != nil {
		_ = legacyStore.Close()
		t.Fatal(err)
	}

	type inviteCodePersistenceSnapshot struct {
		CodeHash          string
		CodePrefix        string
		RegistrationLimit int
		RegistrationCount int
		Enabled           int
		CreatedByUserID   string
		CreatedAt         string
		UpdatedAt         string
	}
	readSnapshot := func(database *sql.DB) (string, inviteCodePersistenceSnapshot) {
		t.Helper()
		var legacyCode string
		var snapshot inviteCodePersistenceSnapshot
		if err := database.QueryRowContext(requestContext, `
			SELECT code, code_hash, code_prefix, registration_limit, registration_count,
			       enabled, created_by_user_id, created_at, updated_at
			FROM invite_codes WHERE id = ?`, inviteCode.ID,
		).Scan(
			&legacyCode,
			&snapshot.CodeHash,
			&snapshot.CodePrefix,
			&snapshot.RegistrationLimit,
			&snapshot.RegistrationCount,
			&snapshot.Enabled,
			&snapshot.CreatedByUserID,
			&snapshot.CreatedAt,
			&snapshot.UpdatedAt,
		); err != nil {
			t.Fatal(err)
		}
		return legacyCode, snapshot
	}

	newInsertCode, preservedSnapshot := readSnapshot(legacyStore.db)
	if newInsertCode != "" {
		_ = legacyStore.Close()
		t.Fatalf("new invite stored legacy plaintext %q", newInsertCode)
	}
	if preservedSnapshot.CodeHash != keyhash.HashAPIKey(rawInviteCode) {
		_ = legacyStore.Close()
		t.Fatalf("stored invite hash = %q, want hash of raw invite", preservedSnapshot.CodeHash)
	}

	redemptionsBeforeMigration, err := legacyStore.ListInviteCodeRedemptionsPage(requestContext, inviteCode.ID, nil, 50)
	if err != nil {
		_ = legacyStore.Close()
		t.Fatal(err)
	}
	if len(redemptionsBeforeMigration.Redemptions) != 1 {
		_ = legacyStore.Close()
		t.Fatalf("redemptions before migration = %d, want 1", len(redemptionsBeforeMigration.Redemptions))
	}
	firstRedemptionID := redemptionsBeforeMigration.Redemptions[0].ID

	// Recreate the pre-009 state: a legacy binary retained the raw value and
	// the plaintext-clearing migration has not yet been recorded.
	if _, err := legacyStore.db.ExecContext(requestContext,
		`UPDATE invite_codes SET code = ? WHERE id = ?`, rawInviteCode, inviteCode.ID,
	); err != nil {
		_ = legacyStore.Close()
		t.Fatal(err)
	}
	if _, err := legacyStore.db.ExecContext(requestContext,
		`DELETE FROM schema_migrations WHERE version = '009_clear_invite_code_plaintext'`,
	); err != nil {
		_ = legacyStore.Close()
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

	clearedCode, migratedSnapshot := readSnapshot(migratedStore.db)
	if clearedCode != "" {
		t.Fatalf("migrated invite plaintext = %q, want empty", clearedCode)
	}
	if migratedSnapshot != preservedSnapshot {
		t.Fatalf("invite metadata changed during plaintext migration:\n got %+v\nwant %+v", migratedSnapshot, preservedSnapshot)
	}

	redemptionsAfterMigration, err := migratedStore.ListInviteCodeRedemptionsPage(requestContext, inviteCode.ID, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(redemptionsAfterMigration.Redemptions) != 1 || redemptionsAfterMigration.Redemptions[0].ID != firstRedemptionID {
		t.Fatalf("redemption audit changed during migration: %+v", redemptionsAfterMigration.Redemptions)
	}

	if _, err := migratedStore.RegisterUserWithInviteCode(
		requestContext,
		"legacy-second-redemption",
		"hash",
		rawInviteCode,
	); err != nil {
		t.Fatalf("redeem original raw invite after plaintext clearing: %v", err)
	}
	migratedInviteCode, err := migratedStore.getInviteCodeByID(requestContext, inviteCode.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migratedInviteCode.RegistrationCount != 2 {
		t.Fatalf("registration count after post-migration redemption = %d, want 2", migratedInviteCode.RegistrationCount)
	}
}

func TestCreateKeyRequiresName(t *testing.T) {
	s := openTestDB(t)
	uid := testUserID(t, s)
	_, _, err := s.CreateKey(context.Background(), uid, "  ", 20)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestListUpdateDeleteKey(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	uid := testUserID(t, s)

	k, _, err := s.CreateKey(ctx, uid, "one", 20)
	if err != nil {
		t.Fatal(err)
	}

	keyPage, err := s.ListKeysByUserPage(ctx, uid, nil, 100)
	if err != nil || len(keyPage.Keys) != 1 {
		t.Fatalf("ListKeysByUserPage: %v len=%d", err, len(keyPage.Keys))
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
	keyPage, err = s.ListKeysByUserPage(ctx, uid, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(keyPage.Keys) != 0 {
		t.Fatalf("expected 0 keys after delete")
	}
}

func TestCreateKeyEnforcesConcurrentPerUserLimitAndDeletionFreesCapacity(t *testing.T) {
	sqliteStore := openTestDB(t)
	requestContext := context.Background()
	userID := testUserID(t, sqliteStore)
	const maximumKeys = 5
	const concurrentAttempts = 20

	var waitGroup sync.WaitGroup
	waitGroup.Add(concurrentAttempts)
	resultErrors := make(chan error, concurrentAttempts)
	for attemptIndex := 0; attemptIndex < concurrentAttempts; attemptIndex++ {
		attemptIndex := attemptIndex
		go func() {
			defer waitGroup.Done()
			_, _, createErr := sqliteStore.CreateKey(
				requestContext,
				userID,
				fmt.Sprintf("concurrent-key-%d", attemptIndex),
				maximumKeys,
			)
			resultErrors <- createErr
		}()
	}
	waitGroup.Wait()
	close(resultErrors)

	successCount := 0
	limitRejectionCount := 0
	for createErr := range resultErrors {
		switch {
		case createErr == nil:
			successCount++
		case errors.Is(createErr, ErrAPIKeyLimit):
			limitRejectionCount++
		default:
			t.Fatalf("unexpected concurrent CreateKey error: %v", createErr)
		}
	}
	if successCount != maximumKeys || limitRejectionCount != concurrentAttempts-maximumKeys {
		t.Fatalf("successes=%d limit_rejections=%d, want %d and %d", successCount, limitRejectionCount, maximumKeys, concurrentAttempts-maximumKeys)
	}

	keyPage, err := sqliteStore.ListKeysByUserPage(requestContext, userID, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if keyPage.TotalCount != maximumKeys {
		t.Fatalf("stored key count = %d, want %d", keyPage.TotalCount, maximumKeys)
	}
	disableKey := false
	if _, err := sqliteStore.UpdateKey(requestContext, keyPage.Keys[0].ID, KeyUpdates{Enabled: &disableKey}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqliteStore.CreateKey(requestContext, userID, "disabled-still-counts", maximumKeys); !errors.Is(err, ErrAPIKeyLimit) {
		t.Fatalf("disabled key should still consume capacity, got %v", err)
	}
	if err := sqliteStore.DeleteKey(requestContext, keyPage.Keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqliteStore.CreateKey(requestContext, userID, "replacement-key", maximumKeys); err != nil {
		t.Fatalf("deletion should free key capacity: %v", err)
	}
}

func TestDeleteKeySucceedsWhenDebugCleanupFails(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	userID := testUserID(t, store)

	key, _, err := store.CreateKey(ctx, userID, "debug-cleanup-failure", 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.debugDB.Close(); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteKey(ctx, key.ID); err != nil {
		t.Fatalf("primary deletion should succeed despite debug cleanup failure: %v", err)
	}
	if _, err := store.GetKeyByID(ctx, key.ID); err == nil {
		t.Fatal("expected API key to be deleted from the primary database")
	}
}

func TestUsageStats(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	k, _, err := s.CreateKey(ctx, testUserID(t, s), "usage", 20)
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

func TestUsageDebugBodiesPersistInSeparateDatabaseWithBoundedBlobs(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	userID := testUserID(t, store)
	key, _, err := store.CreateKey(ctx, userID, "debug-capture", 20)
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

	debugJSON := `{"version":2,"request":{"body_storage":"debug_sqlite"},"response":{"body_storage":"debug_sqlite"}}`
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
	if !persistedRecord.HasDebugRequestBody || persistedRecord.DebugRequestBytes != maxPersistedDebugBodyBytes {
		t.Fatalf("request body summary = available:%v bytes:%d, want true/%d", persistedRecord.HasDebugRequestBody, persistedRecord.DebugRequestBytes, maxPersistedDebugBodyBytes)
	}
	if !persistedRecord.HasDebugResponseBody || persistedRecord.DebugResponseBytes != maxPersistedDebugBodyBytes {
		t.Fatalf("response body summary = available:%v bytes:%d, want true/%d", persistedRecord.HasDebugResponseBody, persistedRecord.DebugResponseBytes, maxPersistedDebugBodyBytes)
	}
	if persistedRecord.DebugRequestObservedBytes != int64(len(requestBody)) || persistedRecord.DebugResponseObservedBytes != int64(len(responseBody)) {
		t.Fatalf("observed body bytes request=%d response=%d, want %d and %d", persistedRecord.DebugRequestObservedBytes, persistedRecord.DebugResponseObservedBytes, len(requestBody), len(responseBody))
	}
	if !persistedRecord.DebugRequestTruncated || !persistedRecord.DebugResponseTruncated {
		t.Fatalf("truncation flags request=%v response=%v, want true", persistedRecord.DebugRequestTruncated, persistedRecord.DebugResponseTruncated)
	}
	if persistedRecord.DebugRequestBodyPath != "" || persistedRecord.DebugResponseBodyPath != "" {
		t.Fatalf("temporary paths must not be returned from stats: %+v", persistedRecord)
	}

	detailRecord, err := store.GetUsageRecordDetail(ctx, persistedRecord.ID, UsageRecordScope{UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if detailRecord.DebugRequestBody != requestBody[:maxPersistedDebugBodyBytes] {
		t.Fatalf("detail request body length = %d, want %d", len(detailRecord.DebugRequestBody), maxPersistedDebugBodyBytes)
	}
	if detailRecord.DebugResponseBody != responseBody[:maxPersistedDebugBodyBytes] {
		t.Fatalf("detail response body length = %d, want %d", len(detailRecord.DebugResponseBody), maxPersistedDebugBodyBytes)
	}

	var debugRecordCount int
	var requestBlobBytes int64
	var responseBlobBytes int64
	if err := store.debugDB.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(length(request_body)), 0), COALESCE(MAX(length(response_body)), 0)
		 FROM usage_debug WHERE usage_id = ?`,
		persistedRecord.ID,
	).Scan(&debugRecordCount, &requestBlobBytes, &responseBlobBytes); err != nil {
		t.Fatal(err)
	}
	if debugRecordCount != 1 {
		t.Fatalf("debug record count = %d, want 1", debugRecordCount)
	}
	if requestBlobBytes != maxPersistedDebugBodyBytes || responseBlobBytes != maxPersistedDebugBodyBytes {
		t.Fatalf("debug blob bytes request=%d response=%d, want %d", requestBlobBytes, responseBlobBytes, maxPersistedDebugBodyBytes)
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
	key, _, err := store.CreateKey(ctx, owner.ID, "owned-key", 20)
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

func TestUsageDebugBodyPersistenceFailurePreservesPrimaryUsage(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	key, _, err := store.CreateKey(ctx, testUserID(t, store), "debug-rollback", 20)
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
	if usageCount != 1 {
		t.Fatalf("usage rows after debug persistence failure = %d, want 1", usageCount)
	}
	var debugRecordCount int
	if err := store.debugDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_debug`).Scan(&debugRecordCount); err != nil {
		t.Fatal(err)
	}
	if debugRecordCount != 0 {
		t.Fatalf("debug records after failed sidecar write = %d, want 0", debugRecordCount)
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

	firstKey, _, err := s.CreateKey(ctx, firstUserID, "first", 20)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, _, err := s.CreateKey(ctx, secondUser.ID, "second", 20)
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
	key, _, err := s.CreateKey(ctx, userID, "high-volume", 20)
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
	if len(stats.Records) != usageRecordPageSize {
		t.Fatalf("expected recent activity to remain limited to %d records, got %d", usageRecordPageSize, len(stats.Records))
	}
	if !stats.RecordsPage.HasMore || stats.RecordsPage.NextCursor == nil {
		t.Fatal("expected high-volume recent activity to expose a continuation cursor")
	}
	firstPage, err := s.ListUsageRecordsPage(ctx, UsageRecordListScope{UserID: userID}, recordTimestamp.Add(-time.Hour), nil, 40)
	if err != nil {
		t.Fatal(err)
	}
	secondPage, err := s.ListUsageRecordsPage(ctx, UsageRecordListScope{UserID: userID}, recordTimestamp.Add(-time.Hour), firstPage.NextCursor, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Records) != 40 || len(secondPage.Records) != 40 {
		t.Fatalf("expected two full keyset pages, got %d and %d records", len(firstPage.Records), len(secondPage.Records))
	}
	seenUsageRecordIDs := make(map[int64]struct{}, 80)
	for _, page := range []*UsageRecordPage{firstPage, secondPage} {
		for recordIndex, record := range page.Records {
			if _, alreadySeen := seenUsageRecordIDs[record.ID]; alreadySeen {
				t.Fatalf("usage record %d appeared in more than one keyset page", record.ID)
			}
			seenUsageRecordIDs[record.ID] = struct{}{}
			if recordIndex > 0 && page.Records[recordIndex-1].ID <= record.ID {
				t.Fatalf("same-timestamp usage records are not ordered by descending ID: %d before %d", page.Records[recordIndex-1].ID, record.ID)
			}
		}
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

func TestAsyncUsageWriterCloseFlushesUsageAndKeyAccounting(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	key, _, err := s.CreateKey(ctx, testUserID(t, s), "async", 20)
	if err != nil {
		t.Fatal(err)
	}

	writer := NewAsyncUsageWriter(s, 8)
	now := time.Now().UTC()
	writer.Enqueue(UsageRecord{KeyID: key.ID, ToolName: "grok_web_search", Timestamp: now, DurationMs: 21, Success: true})
	writer.Enqueue(UsageRecord{KeyID: key.ID, ToolName: "grok_x_search", Timestamp: now, DurationMs: 22, Success: false})
	writer.Close()

	updatedKey, err := s.GetKeyByID(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedKey.TotalCalls != 2 || updatedKey.LastUsedAt == nil {
		t.Fatalf("expected usage records to update key accounting, got %+v", updatedKey)
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

func TestListTiersPageContinuesBeyondFirstHundredWithoutGaps(t *testing.T) {
	sqliteStore := openTestDB(t)
	requestContext := context.Background()

	var finalCreatedTierID string
	for tierNumber := 1; tierNumber <= 101; tierNumber++ {
		tier, err := sqliteStore.CreateTier(
			requestContext,
			fmt.Sprintf("pagination-tier-%03d", tierNumber),
			100+tierNumber,
			tierNumber,
			tierNumber*100,
		)
		if err != nil {
			t.Fatalf("create pagination tier %d: %v", tierNumber, err)
		}
		finalCreatedTierID = tier.ID
	}

	firstPage, err := sqliteStore.ListTiersPage(requestContext, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Tiers) != 100 || !firstPage.HasMore || firstPage.NextCursor == nil {
		t.Fatalf("unexpected first tier page: count=%d has_more=%t next_cursor=%+v", len(firstPage.Tiers), firstPage.HasMore, firstPage.NextCursor)
	}

	secondPage, err := sqliteStore.ListTiersPage(requestContext, firstPage.NextCursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if secondPage.HasMore || secondPage.NextCursor != nil {
		t.Fatalf("unexpected continuation after final tier page: has_more=%t next_cursor=%+v", secondPage.HasMore, secondPage.NextCursor)
	}

	seenTierIdentifiers := make(map[string]struct{}, int(firstPage.TotalCount))
	for _, page := range []*TierPage{firstPage, secondPage} {
		for _, tier := range page.Tiers {
			if _, alreadySeen := seenTierIdentifiers[tier.ID]; alreadySeen {
				t.Fatalf("tier %s appeared on more than one cursor page", tier.ID)
			}
			seenTierIdentifiers[tier.ID] = struct{}{}
		}
	}
	if int64(len(seenTierIdentifiers)) != firstPage.TotalCount {
		t.Fatalf("loaded %d unique tiers across pages, want total_count %d", len(seenTierIdentifiers), firstPage.TotalCount)
	}
	if _, found := seenTierIdentifiers[finalCreatedTierID]; !found {
		t.Fatalf("final created tier %s was not reachable through cursor pagination", finalCreatedTierID)
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
