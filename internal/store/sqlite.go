// sqlite.go 实现 Store 接口：WAL 模式 SQLite、UUID 风格主键、grok_ 前缀的随机 API Key。
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/grok-mcp/internal/keycrypt"
	"github.com/grok-mcp/internal/keyhash"

	_ "modernc.org/sqlite"
)

// timeLayout 为库内 UTC 时间字符串格式（与 SQLite datetime 列一致）。
const timeLayout = "2006-01-02 15:04:05"

const sqliteReadPoolSize = 4

const debugCleanupTimeout = 5 * time.Second

// SQLiteStore 使用纯 Go 驱动 modernc.org/sqlite。写连接保持串行，读取连接池用于发挥 WAL 的并发读取能力。
type SQLiteStore struct {
	db           *sql.DB
	readDB       *sql.DB
	debugDB      *sql.DB
	secretCipher *keycrypt.Cipher
}

const debugDatabaseSuffix = ".debug.sqlite"

func debugDatabasePath(mainDatabasePath string) string {
	if mainDatabasePath == ":memory:" {
		return ":memory:"
	}
	return mainDatabasePath + debugDatabaseSuffix
}

func openDebugSQLite(mainDatabasePath string) (*sql.DB, error) {
	debugPath := debugDatabasePath(mainDatabasePath)
	if debugPath != ":memory:" {
		debugFile, err := os.OpenFile(debugPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create debug sqlite: %w", err)
		}
		if closeErr := debugFile.Close(); closeErr != nil {
			return nil, fmt.Errorf("close debug sqlite file: %w", closeErr)
		}
		if err := os.Chmod(debugPath, 0o600); err != nil {
			return nil, fmt.Errorf("secure debug sqlite file: %w", err)
		}
	}

	debugDB, err := sql.Open("sqlite", debugPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("open debug sqlite: %w", err)
	}
	debugDB.SetMaxOpenConns(1)
	debugDB.SetMaxIdleConns(1)
	if _, err := debugDB.Exec(`
		CREATE TABLE IF NOT EXISTS usage_debug (
			usage_id INTEGER PRIMARY KEY,
			key_id TEXT NOT NULL,
			usage_timestamp TEXT NOT NULL,
			debug_json TEXT NOT NULL DEFAULT '',
			request_body BLOB,
			response_body BLOB,
			request_captured_bytes INTEGER NOT NULL DEFAULT 0,
			response_captured_bytes INTEGER NOT NULL DEFAULT 0,
			request_observed_bytes INTEGER NOT NULL DEFAULT 0,
			response_observed_bytes INTEGER NOT NULL DEFAULT 0,
			request_truncated INTEGER NOT NULL DEFAULT 0,
			response_truncated INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_usage_debug_key_id ON usage_debug(key_id);
		CREATE INDEX IF NOT EXISTS idx_usage_debug_created_at ON usage_debug(created_at);
	`); err != nil {
		_ = debugDB.Close()
		return nil, fmt.Errorf("initialize debug sqlite: %w", err)
	}
	return debugDB, nil
}

// OpenSQLite 打开数据库、执行嵌入迁移并返回可用的 Store。
func OpenSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	debugDB, err := openDebugSQLite(path)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if path == ":memory:" {
		return &SQLiteStore{db: db, readDB: db, debugDB: debugDB}, nil
	}

	readDB, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=query_only(1)")
	if err != nil {
		_ = debugDB.Close()
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite read pool: %w", err)
	}
	readDB.SetMaxOpenConns(sqliteReadPoolSize)
	readDB.SetMaxIdleConns(sqliteReadPoolSize)
	if err := readDB.Ping(); err != nil {
		_ = readDB.Close()
		_ = debugDB.Close()
		_ = db.Close()
		return nil, fmt.Errorf("initialize sqlite read pool: %w", err)
	}

	return &SQLiteStore{db: db, readDB: readDB, debugDB: debugDB}, nil
}

func (s *SQLiteStore) Close() error {
	var firstCloseErr error
	if s.debugDB != nil {
		firstCloseErr = s.debugDB.Close()
	}
	if s.readDB != nil && s.readDB != s.db {
		if err := s.readDB.Close(); err != nil && firstCloseErr == nil {
			firstCloseErr = err
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil && firstCloseErr == nil {
			firstCloseErr = err
		}
	}
	return firstCloseErr
}

// ConfigureAPIKeyEncryption derives the at-rest encryption key used for API
// keys and other persisted secrets.
func (s *SQLiteStore) ConfigureAPIKeyEncryption(applicationSecret string) error {
	secretCipher, err := keycrypt.New(applicationSecret)
	if err != nil {
		return err
	}
	s.secretCipher = secretCipher
	return nil
}

func apiKeyRecordIdentity(keyID, userID string) string {
	return "api-key:" + keyID + ":user:" + userID
}

// randomID 生成 UUID v4 风格的十六进制 ID（无第三方依赖）。
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// generateRawKey 生成 grok_<64 hex> 形态的客户端密钥明文。
func generateRawKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "grok_" + hex.EncodeToString(b), nil
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation(timeLayout, s, time.UTC)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// scanAPIKey 从 QueryRow 或 Rows 扫描一行 apikeys 表记录。
func scanAPIKey(row interface {
	Scan(dest ...any) error
}) (*APIKey, error) {
	var k APIKey
	var enabled int
	var createdAt, updatedAt string
	var lastUsed sql.NullString

	err := row.Scan(
		&k.ID, &k.UserID, &k.Name, &k.KeyHash, &k.KeyPrefix,
		&k.keyCiphertext, &k.keyNonce, &k.keyEncryptionVersion, &enabled,
		&createdAt, &updatedAt, &lastUsed, &k.TotalCalls,
	)
	if err != nil {
		return nil, err
	}
	k.Enabled = enabled != 0
	var err2 error
	k.CreatedAt, err2 = parseTime(createdAt)
	if err2 != nil {
		return nil, err2
	}
	k.UpdatedAt, err2 = parseTime(updatedAt)
	if err2 != nil {
		return nil, err2
	}
	if lastUsed.Valid {
		t, err := parseTime(lastUsed.String)
		if err != nil {
			return nil, err
		}
		k.LastUsedAt = &t
	}
	return &k, nil
}

const keyColumns = `id, user_id, name, key_hash, key_prefix, key_ciphertext, key_nonce, key_encryption_version, enabled, created_at, updated_at, last_used_at, total_calls`

const listKeysByUserQuery = `SELECT ` + keyColumns + ` FROM apikeys WHERE user_id = ? ORDER BY created_at DESC`

// CreateKey 插入新密钥并返回元数据与初始明文 raw；后续可通过 RevealKey 按需恢复。
func (s *SQLiteStore) CreateKey(ctx context.Context, userID, name string) (*APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("name is required")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, "", fmt.Errorf("user_id is required")
	}

	raw, err := generateRawKey()
	if err != nil {
		return nil, "", err
	}

	id, err := randomID()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	prefix := raw
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	ciphertext, nonce, encryptionVersion, err := s.secretCipher.Encrypt(raw, apiKeyRecordIdentity(id, userID))
	if err != nil {
		return nil, "", err
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO apikeys (id, user_id, name, key_hash, key_prefix, key_ciphertext, key_nonce, key_encryption_version, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, userID, name, keyhash.HashAPIKey(raw), prefix, ciphertext, nonce, encryptionVersion, formatTime(now), formatTime(now),
	)
	if err != nil {
		return nil, "", fmt.Errorf("insert apikey: %w", err)
	}

	k, err := s.GetKeyByID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return k, raw, nil
}

// RevealKey decrypts a stored API key. Ownership must be checked by the caller
// before returning the secret to a panel client.
func (s *SQLiteStore) RevealKey(ctx context.Context, id string) (string, error) {
	apiKey, err := s.GetKeyByID(ctx, id)
	if err != nil {
		return "", err
	}
	if apiKey.keyCiphertext == "" || apiKey.keyNonce == "" || apiKey.keyEncryptionVersion == 0 {
		return "", fmt.Errorf("api key secret is unavailable")
	}
	return s.secretCipher.Decrypt(
		apiKey.keyCiphertext,
		apiKey.keyNonce,
		apiKeyRecordIdentity(apiKey.ID, apiKey.UserID),
		apiKey.keyEncryptionVersion,
	)
}

// GetKeyByHash 供鉴权使用；未找到时返回 (nil, nil)。
func (s *SQLiteStore) GetKeyByHash(ctx context.Context, hash string) (*APIKey, error) {
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM apikeys WHERE key_hash = ?`, hash)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (s *SQLiteStore) ListKeysByUser(ctx context.Context, userID string) ([]*APIKey, error) {
	rows, err := s.readDB.QueryContext(ctx, listKeysByUserQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func normalizePanelPageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func (s *SQLiteStore) ListKeysByUserPage(ctx context.Context, userID string, cursor *TimeIDCursor, limit int) (*APIKeyPage, error) {
	pageLimit := normalizePanelPageLimit(limit)
	query := `SELECT ` + keyColumns + ` FROM apikeys WHERE user_id = ?`
	queryArgs := []any{userID}
	if cursor != nil {
		cursorTimestamp := formatTime(cursor.Timestamp.UTC())
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		queryArgs = append(queryArgs, cursorTimestamp, cursorTimestamp, cursor.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	queryArgs = append(queryArgs, pageLimit+1)

	rows, err := s.readDB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := make([]*APIKey, 0, pageLimit+1)
	for rows.Next() {
		apiKey, scanErr := scanAPIKey(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		keys = append(keys, apiKey)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	page := &APIKeyPage{}
	if len(keys) > pageLimit {
		page.HasMore = true
		keys = keys[:pageLimit]
	}
	page.Keys = keys
	if page.HasMore && len(keys) > 0 {
		lastKey := keys[len(keys)-1]
		page.NextCursor = &TimeIDCursor{Timestamp: lastKey.CreatedAt, ID: lastKey.ID}
	}
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END), 0) FROM apikeys WHERE user_id = ?`,
		userID,
	).Scan(&page.TotalCount, &page.ActiveCount); err != nil {
		return nil, err
	}
	return page, nil
}

func (s *SQLiteStore) ListKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT `+keyColumns+` FROM apikeys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *SQLiteStore) GetKeyByID(ctx context.Context, id string) (*APIKey, error) {
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM apikeys WHERE id = ?`, id)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("api key not found")
	}
	return k, err
}

// UpdateKey 动态拼接 SET 子句，仅更新 KeyUpdates 中非 nil 字段。
func (s *SQLiteStore) UpdateKey(ctx context.Context, id string, updates KeyUpdates) (*APIKey, error) {
	if _, err := s.GetKeyByID(ctx, id); err != nil {
		return nil, err
	}

	var sets []string
	var args []any

	if updates.Name != nil {
		name := strings.TrimSpace(*updates.Name)
		if name == "" {
			return nil, fmt.Errorf("name must not be empty")
		}
		sets = append(sets, "name = ?")
		args = append(args, name)
	}
	if updates.Enabled != nil {
		en := 0
		if *updates.Enabled {
			en = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, en)
	}

	if len(sets) == 0 {
		return s.GetKeyByID(ctx, id)
	}

	sets = append(sets, "updated_at = ?")
	args = append(args, formatTime(time.Now().UTC()))
	args = append(args, id)

	q := `UPDATE apikeys SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return nil, err
	}
	return s.GetKeyByID(ctx, id)
}

func (s *SQLiteStore) DeleteKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM apikeys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key not found")
	}
	s.deleteUsageDebugByKeyIDsBestEffort([]string{id})
	return nil
}

// deleteUsageDebugByKeyIDsBestEffort keeps auxiliary debug cleanup from
// changing the result of an already committed primary-database deletion.
func (s *SQLiteStore) deleteUsageDebugByKeyIDsBestEffort(keyIDs []string) {
	if len(keyIDs) == 0 {
		return
	}

	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), debugCleanupTimeout)
	defer cancelCleanup()

	placeholders := make([]string, len(keyIDs))
	arguments := make([]any, len(keyIDs))
	for keyIndex, keyID := range keyIDs {
		placeholders[keyIndex] = "?"
		arguments[keyIndex] = keyID
	}

	_, _ = s.debugDB.ExecContext(cleanupContext,
		`DELETE FROM usage_debug WHERE key_id IN (`+strings.Join(placeholders, ", ")+`)`,
		arguments...,
	)
}

func (s *SQLiteStore) RecordUsage(ctx context.Context, record UsageRecord) error {
	success := 0
	if record.Success {
		success = 1
	}
	usageTimestamp := formatTime(record.Timestamp.UTC())

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()

	result, err := transaction.ExecContext(ctx,
		`INSERT INTO usage_log (key_id, tool_name, timestamp, duration_ms, success) VALUES (?, ?, ?, ?, ?)`,
		record.KeyID, record.ToolName, usageTimestamp, record.DurationMs, success,
	)
	if err != nil {
		return err
	}
	usageID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("usage insert id: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE apikeys
		SET last_used_at = CASE
				WHEN last_used_at IS NULL OR last_used_at < ? THEN ?
				ELSE last_used_at
			END,
			total_calls = total_calls + 1
		WHERE id = ?`,
		usageTimestamp, usageTimestamp, record.KeyID,
	); err != nil {
		return fmt.Errorf("update API key usage: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return err
	}

	// Usage accounting is authoritative in the primary database. Debug capture
	// is persisted afterwards in its own SQLite file so large diagnostic writes
	// cannot expand or lock the primary database transaction.
	if err := s.persistUsageDebugRecord(ctx, usageID, record); err != nil {
		return fmt.Errorf("persist usage debug record: %w", err)
	}
	return nil
}

const maxPersistedDebugBodyBytes int64 = 1 << 20

func readBoundedDebugBody(path string) ([]byte, int64, bool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, 0, false, nil
	}
	bodyFile, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer bodyFile.Close()
	bodyInfo, err := bodyFile.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	observedBytes := bodyInfo.Size()

	body, err := io.ReadAll(io.LimitReader(bodyFile, maxPersistedDebugBodyBytes+1))
	if err != nil {
		return nil, 0, false, err
	}
	if int64(len(body)) > maxPersistedDebugBodyBytes {
		return body[:maxPersistedDebugBodyBytes], observedBytes, true, nil
	}
	return body, observedBytes, false, nil
}

func boolAsInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *SQLiteStore) persistUsageDebugRecord(ctx context.Context, usageID int64, record UsageRecord) error {
	if strings.TrimSpace(record.DebugJSON) == "" &&
		strings.TrimSpace(record.DebugRequestBodyPath) == "" &&
		strings.TrimSpace(record.DebugResponseBodyPath) == "" {
		return nil
	}

	requestBody, requestSpoolBytes, requestPersistenceTruncated, err := readBoundedDebugBody(record.DebugRequestBodyPath)
	if err != nil {
		return fmt.Errorf("read request debug body: %w", err)
	}
	responseBody, responseSpoolBytes, responsePersistenceTruncated, err := readBoundedDebugBody(record.DebugResponseBodyPath)
	if err != nil {
		return fmt.Errorf("read response debug body: %w", err)
	}

	requestObservedBytes := record.DebugRequestObservedBytes
	if requestObservedBytes < requestSpoolBytes {
		requestObservedBytes = requestSpoolBytes
	}
	responseObservedBytes := record.DebugResponseObservedBytes
	if responseObservedBytes < responseSpoolBytes {
		responseObservedBytes = responseSpoolBytes
	}

	_, err = s.debugDB.ExecContext(ctx, `
		INSERT INTO usage_debug (
			usage_id, key_id, usage_timestamp, debug_json,
			request_body, response_body,
			request_captured_bytes, response_captured_bytes,
			request_observed_bytes, response_observed_bytes,
			request_truncated, response_truncated, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(usage_id) DO UPDATE SET
			key_id = excluded.key_id,
			usage_timestamp = excluded.usage_timestamp,
			debug_json = excluded.debug_json,
			request_body = excluded.request_body,
			response_body = excluded.response_body,
			request_captured_bytes = excluded.request_captured_bytes,
			response_captured_bytes = excluded.response_captured_bytes,
			request_observed_bytes = excluded.request_observed_bytes,
			response_observed_bytes = excluded.response_observed_bytes,
			request_truncated = excluded.request_truncated,
			response_truncated = excluded.response_truncated,
			created_at = excluded.created_at`,
		usageID,
		record.KeyID,
		formatTime(record.Timestamp.UTC()),
		record.DebugJSON,
		requestBody,
		responseBody,
		len(requestBody),
		len(responseBody),
		requestObservedBytes,
		responseObservedBytes,
		boolAsInteger(record.DebugRequestTruncated || requestPersistenceTruncated),
		boolAsInteger(record.DebugResponseTruncated || responsePersistenceTruncated),
		formatTime(time.Now().UTC()),
	)
	return err
}

// TouchKeyUsage 保留给直接调用方；异步用量生产路径通过 RecordUsage 原子更新计数。
// 不触碰 updated_at——该字段只随 CreateKey/UpdateKey 的配置变更而更新。
func (s *SQLiteStore) TouchKeyUsage(ctx context.Context, keyID string) error {
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx,
		`UPDATE apikeys SET last_used_at = ?, total_calls = total_calls + 1 WHERE id = ?`,
		now, keyID,
	)
	return err
}

// usageStatsScope 限定 usage_log 聚合的 WHERE 条件，禁止任意 SQL 片段拼接。
type usageStatsScope int

const (
	usageStatsByKey usageStatsScope = iota
	usageStatsByUser
	usageStatsGlobal
)

var usageStatsWhere = map[usageStatsScope]string{
	usageStatsByKey:  `key_id = ?`,
	usageStatsByUser: `key_id IN (SELECT id FROM apikeys WHERE user_id = ?)`,
	usageStatsGlobal: `1=1`,
}

func buildUsageStatsAggregateQuery(where string) string {
	return `SELECT tool_name, COUNT(*), COALESCE(SUM(success), 0) FROM usage_log WHERE ` +
		where + ` AND timestamp >= ? GROUP BY tool_name`
}

type usageRollupSource struct {
	tableName       string
	timestampColumn string
	bucketDuration  time.Duration
}

var usageRollupSources = []usageRollupSource{
	{
		tableName:       "usage_hourly_rollups",
		timestampColumn: "bucket_start",
		bucketDuration:  time.Hour,
	},
	{
		tableName:       "usage_daily_rollups",
		timestampColumn: "bucket_start",
		bucketDuration:  24 * time.Hour,
	},
}

func buildUsageRollupStatsAggregateQuery(source usageRollupSource, where string) string {
	return `SELECT tool_name, COALESCE(SUM(total_calls), 0), COALESCE(SUM(success_calls), 0) FROM ` +
		source.tableName + ` WHERE ` + where + ` AND ` + source.timestampColumn + ` >= ? GROUP BY tool_name`
}

func (s *SQLiteStore) GetUsageStats(ctx context.Context, keyID string, since time.Time) (*UsageStats, error) {
	return s.queryUsageStats(ctx, usageStatsByKey, []any{keyID}, since, nil, usageRecordPageSize)
}

func (s *SQLiteStore) GetUserUsageStats(ctx context.Context, userID string, since time.Time) (*UsageStats, error) {
	return s.GetUserUsageStatsPage(ctx, userID, since, nil, usageRecordPageSize)
}

func (s *SQLiteStore) GetUserUsageStatsPage(
	ctx context.Context,
	userID string,
	since time.Time,
	cursor *UsageRecordCursor,
	limit int,
) (*UsageStats, error) {
	return s.queryUsageStats(ctx, usageStatsByUser, []any{userID}, since, cursor, limit)
}

func (s *SQLiteStore) GetGlobalStats(ctx context.Context, since time.Time) (*UsageStats, error) {
	return s.queryUsageStats(ctx, usageStatsGlobal, nil, since, nil, usageRecordPageSize)
}

const (
	usageTrafficBucketCount = 8
	usageRecordPageSize     = 50
)

// queryUsageStats 按条件聚合 usage_log，并拉取请求的明细页（按时间与 ID 倒序）。
// 流量桶与最近一分钟调用数均直接由 SQLite 对完整数据集聚合，避免被明细上限截断。
func (s *SQLiteStore) queryUsageStats(
	ctx context.Context,
	scope usageStatsScope,
	whereArgs []any,
	since time.Time,
	recordCursor *UsageRecordCursor,
	recordLimit int,
) (*UsageStats, error) {
	where, ok := usageStatsWhere[scope]
	if !ok {
		return nil, fmt.Errorf("invalid usage stats scope")
	}
	stats := &UsageStats{ByTool: make(map[string]int64)}

	queryEnd := time.Now().UTC().Truncate(time.Second)
	sinceUTC := since.UTC().Truncate(time.Second)
	sinceStr := formatTime(sinceUTC)
	args := appendUsageStatsArgs(whereArgs, sinceStr)

	if err := s.addRawUsageAggregates(ctx, stats, where, args); err != nil {
		return nil, err
	}
	for _, source := range usageRollupSources {
		rollupSince := truncateUsageRollupBoundary(sinceUTC, source.bucketDuration)
		rollupArgs := appendUsageStatsArgs(whereArgs, formatTime(rollupSince))
		if err := s.addRollupUsageAggregates(ctx, stats, source, where, rollupArgs); err != nil {
			return nil, err
		}
	}

	recordPage, err := s.queryUsageRecordPage(ctx, where, whereArgs, sinceUTC, recordCursor, recordLimit)
	if err != nil {
		return nil, err
	}
	stats.Records = recordPage.Records
	stats.RecordsPage = UsageRecordPageInfo{HasMore: recordPage.HasMore, NextCursor: recordPage.NextCursor}
	currentRPM, err := s.queryCurrentRPM(ctx, where, whereArgs, queryEnd)
	if err != nil {
		return nil, err
	}
	stats.CurrentRPM = currentRPM

	trafficRangeStart, err := s.resolveUsageTrafficRangeStart(ctx, where, whereArgs, sinceUTC, queryEnd)
	if err != nil {
		return nil, err
	}
	trafficBuckets, err := s.queryUsageTrafficBuckets(ctx, where, whereArgs, trafficRangeStart, queryEnd)
	if err != nil {
		return nil, err
	}
	stats.TrafficBuckets = trafficBuckets
	return stats, nil
}

func (s *SQLiteStore) ListUsageRecordsPage(
	ctx context.Context,
	scope UsageRecordListScope,
	since time.Time,
	cursor *UsageRecordCursor,
	limit int,
) (*UsageRecordPage, error) {
	where := ""
	whereArgs := make([]any, 0, 1)
	switch {
	case strings.TrimSpace(scope.KeyID) != "":
		where = usageStatsWhere[usageStatsByKey]
		whereArgs = append(whereArgs, strings.TrimSpace(scope.KeyID))
	case scope.IncludeAllUsers:
		where = usageStatsWhere[usageStatsGlobal]
	case strings.TrimSpace(scope.UserID) != "":
		where = usageStatsWhere[usageStatsByUser]
		whereArgs = append(whereArgs, strings.TrimSpace(scope.UserID))
	default:
		return nil, fmt.Errorf("usage record list scope is required")
	}
	return s.queryUsageRecordPage(ctx, where, whereArgs, since.UTC().Truncate(time.Second), cursor, limit)
}

func (s *SQLiteStore) queryUsageRecordPage(
	ctx context.Context,
	where string,
	whereArgs []any,
	since time.Time,
	cursor *UsageRecordCursor,
	limit int,
) (*UsageRecordPage, error) {
	pageLimit := normalizePanelPageLimit(limit)
	query := `SELECT id, key_id, tool_name, timestamp, duration_ms, success
		FROM usage_log WHERE ` + where + ` AND timestamp >= ?`
	queryArgs := appendUsageStatsArgs(whereArgs, formatTime(since.UTC()))
	if cursor != nil {
		cursorTimestamp := formatTime(cursor.Timestamp.UTC())
		query += ` AND (timestamp < ? OR (timestamp = ? AND id < ?))`
		queryArgs = append(queryArgs, cursorTimestamp, cursorTimestamp, cursor.ID)
	}
	query += ` ORDER BY timestamp DESC, id DESC LIMIT ?`
	queryArgs = append(queryArgs, pageLimit+1)

	rows, err := s.readDB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]UsageRecord, 0, pageLimit+1)
	for rows.Next() {
		var record UsageRecord
		var timestamp string
		var success int
		if err := rows.Scan(&record.ID, &record.KeyID, &record.ToolName, &timestamp, &record.DurationMs, &success); err != nil {
			return nil, err
		}
		record.Success = success != 0
		record.Timestamp, err = parseTime(timestamp)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	page := &UsageRecordPage{}
	if len(records) > pageLimit {
		page.HasMore = true
		records = records[:pageLimit]
	}
	page.Records = records
	if page.HasMore && len(records) > 0 {
		lastRecord := records[len(records)-1]
		page.NextCursor = &UsageRecordCursor{Timestamp: lastRecord.Timestamp, ID: lastRecord.ID}
	}
	if err := s.loadUsageDebugBodySummaries(ctx, page.Records); err != nil {
		return nil, err
	}
	return page, nil
}

func (s *SQLiteStore) addRawUsageAggregates(
	ctx context.Context,
	stats *UsageStats,
	where string,
	queryArgs []any,
) error {
	rows, err := s.readDB.QueryContext(ctx, buildUsageStatsAggregateQuery(where), queryArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var toolName string
		var callCount int64
		var successCount int64
		if err := rows.Scan(&toolName, &callCount, &successCount); err != nil {
			return err
		}
		addUsageAggregate(stats, toolName, callCount, successCount)
	}
	return rows.Err()
}

func (s *SQLiteStore) addRollupUsageAggregates(
	ctx context.Context,
	stats *UsageStats,
	source usageRollupSource,
	where string,
	queryArgs []any,
) error {
	rows, err := s.readDB.QueryContext(ctx, buildUsageRollupStatsAggregateQuery(source, where), queryArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var toolName string
		var callCount int64
		var successCount int64
		if err := rows.Scan(&toolName, &callCount, &successCount); err != nil {
			return err
		}
		addUsageAggregate(stats, toolName, callCount, successCount)
	}
	return rows.Err()
}

func addUsageAggregate(stats *UsageStats, toolName string, callCount, successCount int64) {
	stats.ByTool[toolName] += callCount
	stats.TotalCalls += callCount
	stats.SuccessCalls += successCount
}

func truncateUsageRollupBoundary(timestamp time.Time, bucketDuration time.Duration) time.Time {
	if bucketDuration == 24*time.Hour {
		return truncateToUTCDay(timestamp)
	}
	return timestamp.UTC().Truncate(bucketDuration)
}

func (s *SQLiteStore) loadUsageDebugBodySummaries(ctx context.Context, records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}

	queryPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(records)), ",")
	queryArgs := make([]any, 0, len(records))
	recordIndexesByID := make(map[int64]int, len(records))
	for recordIndex := range records {
		queryArgs = append(queryArgs, records[recordIndex].ID)
		recordIndexesByID[records[recordIndex].ID] = recordIndex
	}

	rows, err := s.debugDB.QueryContext(ctx,
		`SELECT usage_id, debug_json,
		        request_captured_bytes, response_captured_bytes,
		        request_observed_bytes, response_observed_bytes,
		        request_truncated, response_truncated
		 FROM usage_debug
		 WHERE usage_id IN (`+queryPlaceholders+`)`,
		queryArgs...,
	)
	if err != nil {
		return err
	}
	for rows.Next() {
		var usageID int64
		var debugJSON string
		var requestBytes int64
		var responseBytes int64
		var requestObservedBytes int64
		var responseObservedBytes int64
		var requestTruncated int
		var responseTruncated int
		if err := rows.Scan(
			&usageID,
			&debugJSON,
			&requestBytes,
			&responseBytes,
			&requestObservedBytes,
			&responseObservedBytes,
			&requestTruncated,
			&responseTruncated,
		); err != nil {
			return err
		}
		recordIndex, exists := recordIndexesByID[usageID]
		if !exists {
			continue
		}
		record := &records[recordIndex]
		record.DebugJSON = debugJSON
		record.HasDebugRequestBody = requestBytes > 0
		record.HasDebugResponseBody = responseBytes > 0
		record.DebugRequestBytes = requestBytes
		record.DebugResponseBytes = responseBytes
		record.DebugRequestObservedBytes = requestObservedBytes
		record.DebugResponseObservedBytes = responseObservedBytes
		record.DebugRequestTruncated = requestTruncated != 0
		record.DebugResponseTruncated = responseTruncated != 0
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) GetUsageRecordDetail(ctx context.Context, usageID int64, scope UsageRecordScope) (*UsageRecord, error) {
	if usageID <= 0 {
		return nil, ErrUsageRecordNotFound
	}

	query := `SELECT usage_log.id, usage_log.key_id, usage_log.tool_name, usage_log.timestamp,
	                 usage_log.duration_ms, usage_log.success
	          FROM usage_log
	          INNER JOIN apikeys ON apikeys.id = usage_log.key_id
	          WHERE usage_log.id = ?`
	queryArgs := []any{usageID}
	if !scope.IncludeAllUsers {
		query += ` AND apikeys.user_id = ?`
		queryArgs = append(queryArgs, scope.UserID)
	}

	var record UsageRecord
	var timestamp string
	var success int
	err := s.readDB.QueryRowContext(ctx, query, queryArgs...).Scan(
		&record.ID,
		&record.KeyID,
		&record.ToolName,
		&timestamp,
		&record.DurationMs,
		&success,
	)
	if err == sql.ErrNoRows {
		return nil, ErrUsageRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	record.Success = success != 0
	record.Timestamp, err = parseTime(timestamp)
	if err != nil {
		return nil, err
	}

	if _, err := s.loadUsageDebugRecord(ctx, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *SQLiteStore) loadUsageDebugRecord(ctx context.Context, record *UsageRecord) (bool, error) {
	var requestBody []byte
	var responseBody []byte
	var requestTruncated int
	var responseTruncated int
	err := s.debugDB.QueryRowContext(ctx, `
		SELECT debug_json, request_body, response_body,
		       request_captured_bytes, response_captured_bytes,
		       request_observed_bytes, response_observed_bytes,
		       request_truncated, response_truncated
		FROM usage_debug
		WHERE usage_id = ? AND key_id = ? AND usage_timestamp = ?`,
		record.ID,
		record.KeyID,
		formatTime(record.Timestamp.UTC()),
	).Scan(
		&record.DebugJSON,
		&requestBody,
		&responseBody,
		&record.DebugRequestBytes,
		&record.DebugResponseBytes,
		&record.DebugRequestObservedBytes,
		&record.DebugResponseObservedBytes,
		&requestTruncated,
		&responseTruncated,
	)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	record.HasDebugRequestBody = record.DebugRequestBytes > 0
	record.HasDebugResponseBody = record.DebugResponseBytes > 0
	record.DebugRequestTruncated = requestTruncated != 0
	record.DebugResponseTruncated = responseTruncated != 0
	record.DebugRequestBody = string(requestBody)
	record.DebugResponseBody = string(responseBody)
	return true, nil
}

func appendUsageStatsArgs(whereArgs []any, trailingArgs ...any) []any {
	queryArgs := make([]any, 0, len(whereArgs)+len(trailingArgs))
	queryArgs = append(queryArgs, whereArgs...)
	queryArgs = append(queryArgs, trailingArgs...)
	return queryArgs
}

func (s *SQLiteStore) queryCurrentRPM(ctx context.Context, where string, whereArgs []any, queryEnd time.Time) (int64, error) {
	queryStart := queryEnd.Add(-time.Minute)
	queryArgs := appendUsageStatsArgs(whereArgs, formatTime(queryStart), formatTime(queryEnd))

	var currentRPM int64
	err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_log WHERE `+where+` AND timestamp >= ? AND timestamp <= ?`,
		queryArgs...,
	).Scan(&currentRPM)
	return currentRPM, err
}

func (s *SQLiteStore) resolveUsageTrafficRangeStart(
	ctx context.Context,
	where string,
	whereArgs []any,
	since time.Time,
	queryEnd time.Time,
) (time.Time, error) {
	if !since.IsZero() {
		if since.Before(queryEnd) {
			return since, nil
		}
		return queryEnd.Add(-24 * time.Hour), nil
	}

	earliestUsageTime, hasUsage, err := s.queryEarliestUsageTime(ctx, "usage_log", "timestamp", where, whereArgs)
	if err != nil {
		return time.Time{}, err
	}
	for _, source := range usageRollupSources {
		sourceEarliestTime, sourceHasUsage, err := s.queryEarliestUsageTime(
			ctx,
			source.tableName,
			source.timestampColumn,
			where,
			whereArgs,
		)
		if err != nil {
			return time.Time{}, err
		}
		if sourceHasUsage && (!hasUsage || sourceEarliestTime.Before(earliestUsageTime)) {
			earliestUsageTime = sourceEarliestTime
			hasUsage = true
		}
	}
	if !hasUsage {
		return queryEnd.Add(-24 * time.Hour), nil
	}

	earliestUsageTime = earliestUsageTime.UTC().Truncate(time.Second)
	if !earliestUsageTime.Before(queryEnd) {
		return queryEnd.Add(-24 * time.Hour), nil
	}
	return earliestUsageTime, nil
}

func (s *SQLiteStore) queryEarliestUsageTime(
	ctx context.Context,
	tableName string,
	timestampColumn string,
	where string,
	whereArgs []any,
) (time.Time, bool, error) {
	var earliestTimestamp sql.NullString
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT MIN(`+timestampColumn+`) FROM `+tableName+` WHERE `+where,
		whereArgs...,
	).Scan(&earliestTimestamp); err != nil {
		return time.Time{}, false, err
	}
	if !earliestTimestamp.Valid || earliestTimestamp.String == "" {
		return time.Time{}, false, nil
	}

	parsedTimestamp, err := parseTime(earliestTimestamp.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return parsedTimestamp, true, nil
}

func (s *SQLiteStore) queryUsageTrafficBuckets(
	ctx context.Context,
	where string,
	whereArgs []any,
	rangeStart time.Time,
	rangeEnd time.Time,
) ([]UsageBucket, error) {
	rangeStart = rangeStart.UTC().Truncate(time.Second)
	rangeEnd = rangeEnd.UTC().Truncate(time.Second)
	if !rangeStart.Before(rangeEnd) {
		rangeStart = rangeEnd.Add(-24 * time.Hour)
	}

	rangeDurationSeconds := int64(rangeEnd.Sub(rangeStart) / time.Second)
	if rangeDurationSeconds < 1 {
		rangeDurationSeconds = 1
	}

	buckets := createEmptyUsageTrafficBuckets(rangeStart, rangeEnd, rangeDurationSeconds)
	if err := s.addUsageTrafficSource(
		ctx,
		buckets,
		"usage_log",
		"timestamp",
		"COUNT(*)",
		where,
		whereArgs,
		rangeStart,
		rangeEnd,
		rangeDurationSeconds,
	); err != nil {
		return nil, err
	}
	for _, source := range usageRollupSources {
		sourceRangeStart := truncateUsageRollupBoundary(rangeStart, source.bucketDuration)
		if err := s.addUsageTrafficSource(
			ctx,
			buckets,
			source.tableName,
			source.timestampColumn,
			"SUM(total_calls)",
			where,
			whereArgs,
			sourceRangeStart,
			rangeEnd,
			rangeDurationSeconds,
		); err != nil {
			return nil, err
		}
	}
	return buckets, nil
}

func (s *SQLiteStore) addUsageTrafficSource(
	ctx context.Context,
	buckets []UsageBucket,
	tableName string,
	timestampColumn string,
	callCountExpression string,
	where string,
	whereArgs []any,
	sourceRangeStart time.Time,
	rangeEnd time.Time,
	rangeDurationSeconds int64,
) error {
	queryArgs := make([]any, 0, 2+len(whereArgs)+2)
	queryArgs = append(queryArgs, buckets[0].Start.Unix(), rangeDurationSeconds)
	queryArgs = append(queryArgs, whereArgs...)
	queryArgs = append(queryArgs, formatTime(sourceRangeStart), formatTime(rangeEnd))

	rows, err := s.readDB.QueryContext(ctx,
		`WITH bucket_window(start_unix, duration_seconds) AS (VALUES (?, ?))
		 SELECT MIN(7, MAX(0, CAST(((CAST(strftime('%s', `+timestampColumn+`) AS INTEGER) - bucket_window.start_unix) * 8) / bucket_window.duration_seconds AS INTEGER))) AS bucket_index,
		        `+callCountExpression+`
		 FROM `+tableName+`
		 CROSS JOIN bucket_window
		 WHERE `+where+` AND `+timestampColumn+` >= ? AND `+timestampColumn+` <= ?
		 GROUP BY bucket_index
		 ORDER BY bucket_index`,
		queryArgs...,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var bucketIndex int
		var callCount int64
		if err := rows.Scan(&bucketIndex, &callCount); err != nil {
			return err
		}
		if bucketIndex < 0 || bucketIndex >= len(buckets) {
			return fmt.Errorf("invalid usage traffic bucket index %d", bucketIndex)
		}
		buckets[bucketIndex].Calls += callCount
	}
	return rows.Err()
}

func createEmptyUsageTrafficBuckets(rangeStart, rangeEnd time.Time, rangeDurationSeconds int64) []UsageBucket {
	buckets := make([]UsageBucket, usageTrafficBucketCount)
	for bucketIndex := 0; bucketIndex < usageTrafficBucketCount; bucketIndex++ {
		bucketStartOffset := divideCeiling(
			rangeDurationSeconds*int64(bucketIndex),
			usageTrafficBucketCount,
		)
		bucketEndOffset := divideCeiling(
			rangeDurationSeconds*int64(bucketIndex+1),
			usageTrafficBucketCount,
		)
		buckets[bucketIndex] = UsageBucket{
			Start: rangeStart.Add(time.Duration(bucketStartOffset) * time.Second),
			End:   rangeStart.Add(time.Duration(bucketEndOffset) * time.Second),
		}
	}
	buckets[len(buckets)-1].End = rangeEnd
	return buckets
}

func divideCeiling(numerator int64, denominator int) int64 {
	return (numerator + int64(denominator) - 1) / int64(denominator)
}

const serverSettingsID = "default"

type storedServerSettingsAPIKey struct {
	ciphertext        string
	nonce             string
	encryptionVersion int
}

func scanServerSettings(row interface {
	Scan(dest ...any) error
}) (*ServerSettings, storedServerSettingsAPIKey, error) {
	var settings ServerSettings
	var storedAPIKey storedServerSettingsAPIKey
	var proxyEnabled int
	var registrationMode string
	var debug int
	var createdAt string
	var updatedAt string
	err := row.Scan(
		&settings.ID,
		&settings.CPABaseURL,
		&storedAPIKey.ciphertext,
		&storedAPIKey.nonce,
		&storedAPIKey.encryptionVersion,
		&settings.UpstreamProtocol,
		&settings.Model,
		&settings.TimeoutSeconds,
		&settings.MCPGlobalSearchConcurrency,
		&settings.MCPUserSearchConcurrency,
		&settings.ProxyURL,
		&proxyEnabled,
		&registrationMode,
		&debug,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, storedServerSettingsAPIKey{}, err
	}
	settings.ProxyEnabled = proxyEnabled != 0
	var normalizeErr error
	settings.RegistrationMode, normalizeErr = NormalizeRegistrationMode(RegistrationMode(registrationMode))
	if normalizeErr != nil {
		return nil, storedServerSettingsAPIKey{}, normalizeErr
	}
	settings.Debug = debug != 0
	var parseErr error
	settings.CreatedAt, parseErr = parseTime(createdAt)
	if parseErr != nil {
		return nil, storedServerSettingsAPIKey{}, parseErr
	}
	settings.UpdatedAt, parseErr = parseTime(updatedAt)
	if parseErr != nil {
		return nil, storedServerSettingsAPIKey{}, parseErr
	}
	return &settings, storedAPIKey, nil
}

const serverSettingsColumns = `id, cpa_base_url, cpa_api_key_ciphertext, cpa_api_key_nonce, cpa_api_key_encryption_version, upstream_protocol, model, timeout_seconds, mcp_global_search_concurrency, mcp_user_search_concurrency, proxy_url, proxy_enabled, registration_mode, debug, created_at, updated_at`

func serverSettingsAPIKeyRecordIdentity(settingsID string) string {
	return "server-settings:" + settingsID + ":cpa-api-key"
}

func (s *SQLiteStore) GetServerSettings(ctx context.Context) (*ServerSettings, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+serverSettingsColumns+` FROM server_settings WHERE id = ?`, serverSettingsID)
	settings, storedAPIKey, err := scanServerSettings(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	plaintextAPIKey, err := s.decryptServerSettingsAPIKey(settings.ID, storedAPIKey)
	if err != nil {
		return nil, err
	}
	settings.CPAAPIKey = plaintextAPIKey
	return settings, nil
}

func (s *SQLiteStore) decryptServerSettingsAPIKey(settingsID string, storedAPIKey storedServerSettingsAPIKey) (string, error) {
	hasCompleteCiphertext := storedAPIKey.ciphertext != "" && storedAPIKey.nonce != "" && storedAPIKey.encryptionVersion != 0
	if !hasCompleteCiphertext {
		return "", fmt.Errorf("server settings CPA API key ciphertext is incomplete")
	}
	plaintextAPIKey, err := s.secretCipher.Decrypt(
		storedAPIKey.ciphertext,
		storedAPIKey.nonce,
		serverSettingsAPIKeyRecordIdentity(settingsID),
		storedAPIKey.encryptionVersion,
	)
	if err != nil {
		return "", fmt.Errorf("decrypt server settings CPA API key: %w", err)
	}
	return plaintextAPIKey, nil
}

func (s *SQLiteStore) UpsertServerSettings(ctx context.Context, settings ServerSettings) (*ServerSettings, error) {
	cpaBaseURL := strings.TrimSpace(settings.CPABaseURL)
	cpaAPIKey := strings.TrimSpace(settings.CPAAPIKey)
	upstreamProtocol := strings.TrimSpace(settings.UpstreamProtocol)
	model := strings.TrimSpace(settings.Model)
	proxyURL := strings.TrimSpace(settings.ProxyURL)
	if cpaBaseURL == "" {
		return nil, fmt.Errorf("cpa_base_url is required")
	}
	if cpaAPIKey == "" {
		return nil, fmt.Errorf("cpa_api_key is required")
	}
	if upstreamProtocol == "" {
		return nil, fmt.Errorf("upstream_protocol is required")
	}
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if settings.TimeoutSeconds <= 0 {
		return nil, fmt.Errorf("timeout_seconds must be positive")
	}
	if settings.MCPGlobalSearchConcurrency <= 0 {
		return nil, fmt.Errorf("mcp_global_search_concurrency must be positive")
	}
	if settings.MCPUserSearchConcurrency <= 0 {
		return nil, fmt.Errorf("mcp_user_search_concurrency must be positive")
	}
	if settings.MCPUserSearchConcurrency > settings.MCPGlobalSearchConcurrency {
		return nil, fmt.Errorf("mcp_user_search_concurrency must not exceed mcp_global_search_concurrency")
	}
	registrationMode, err := NormalizeRegistrationMode(settings.RegistrationMode)
	if err != nil {
		return nil, err
	}
	ciphertext, nonce, encryptionVersion, err := s.secretCipher.Encrypt(
		cpaAPIKey,
		serverSettingsAPIKeyRecordIdentity(serverSettingsID),
	)
	if err != nil {
		return nil, fmt.Errorf("encrypt server settings CPA API key: %w", err)
	}

	proxyEnabled := 0
	if settings.ProxyEnabled {
		proxyEnabled = 1
	}
	debug := 0
	if settings.Debug {
		debug = 1
	}
	now := formatTime(time.Now().UTC())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO server_settings (
			id, cpa_base_url, cpa_api_key_ciphertext, cpa_api_key_nonce, cpa_api_key_encryption_version,
			upstream_protocol, model, timeout_seconds, mcp_global_search_concurrency, mcp_user_search_concurrency,
			proxy_url, proxy_enabled, registration_mode, debug, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			cpa_base_url = excluded.cpa_base_url,
			cpa_api_key_ciphertext = excluded.cpa_api_key_ciphertext,
			cpa_api_key_nonce = excluded.cpa_api_key_nonce,
			cpa_api_key_encryption_version = excluded.cpa_api_key_encryption_version,
			upstream_protocol = excluded.upstream_protocol,
			model = excluded.model,
			timeout_seconds = excluded.timeout_seconds,
			mcp_global_search_concurrency = excluded.mcp_global_search_concurrency,
			mcp_user_search_concurrency = excluded.mcp_user_search_concurrency,
			proxy_url = excluded.proxy_url,
			proxy_enabled = excluded.proxy_enabled,
			registration_mode = excluded.registration_mode,
			debug = excluded.debug,
			updated_at = excluded.updated_at`,
		serverSettingsID,
		cpaBaseURL,
		ciphertext,
		nonce,
		encryptionVersion,
		upstreamProtocol,
		model,
		settings.TimeoutSeconds,
		settings.MCPGlobalSearchConcurrency,
		settings.MCPUserSearchConcurrency,
		proxyURL,
		proxyEnabled,
		string(registrationMode),
		debug,
		now,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert server settings: %w", err)
	}
	return s.GetServerSettings(ctx)
}
