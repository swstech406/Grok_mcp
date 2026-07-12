// sqlite.go 实现 Store 接口：WAL 模式 SQLite、UUID 风格主键、grok_ 前缀的随机 API Key。
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/grok-mcp/internal/keycrypt"
	"github.com/grok-mcp/internal/keyhash"

	_ "modernc.org/sqlite"
)

// timeLayout 为库内 UTC 时间字符串格式（与 SQLite datetime 列一致）。
const timeLayout = "2006-01-02 15:04:05"

// SQLiteStore 使用纯 Go 驱动 modernc.org/sqlite，MaxOpenConns=1 以配合 SQLite 写锁语义。
type SQLiteStore struct {
	db           *sql.DB
	apiKeyCipher *keycrypt.Cipher
}

// OpenSQLite 打开数据库、执行嵌入迁移并返回可用的 Store。
func OpenSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ConfigureAPIKeyEncryption derives the at-rest encryption key used for
// recoverable API key material. Call this before creating, rotating, or
// revealing API keys.
func (s *SQLiteStore) ConfigureAPIKeyEncryption(applicationSecret string) error {
	apiKeyCipher, err := keycrypt.New(applicationSecret)
	if err != nil {
		return err
	}
	s.apiKeyCipher = apiKeyCipher
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
	ciphertext, nonce, encryptionVersion, err := s.apiKeyCipher.Encrypt(raw, apiKeyRecordIdentity(id, userID))
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
	return s.apiKeyCipher.Decrypt(
		apiKey.keyCiphertext,
		apiKey.keyNonce,
		apiKeyRecordIdentity(apiKey.ID, apiKey.UserID),
		apiKey.keyEncryptionVersion,
	)
}

// RotateLegacyAPIKeys replaces hash-only or no-longer-decryptable keys with
// recoverable encrypted keys. The record identity, metadata, and usage history
// are preserved, while the old bearer token stops authenticating immediately.
func (s *SQLiteStore) RotateLegacyAPIKeys(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, key_ciphertext, key_nonce, key_encryption_version FROM apikeys ORDER BY created_at ASC`)
	if err != nil {
		return 0, err
	}

	type legacyAPIKey struct {
		id                   string
		userID               string
		keyCiphertext        string
		keyNonce             string
		keyEncryptionVersion int
	}
	legacyKeys := make([]legacyAPIKey, 0)
	for rows.Next() {
		var legacyKey legacyAPIKey
		if err := rows.Scan(
			&legacyKey.id,
			&legacyKey.userID,
			&legacyKey.keyCiphertext,
			&legacyKey.keyNonce,
			&legacyKey.keyEncryptionVersion,
		); err != nil {
			_ = rows.Close()
			return 0, err
		}
		secretUnavailable := legacyKey.keyCiphertext == "" || legacyKey.keyNonce == "" || legacyKey.keyEncryptionVersion == 0
		if !secretUnavailable {
			_, decryptErr := s.apiKeyCipher.Decrypt(
				legacyKey.keyCiphertext,
				legacyKey.keyNonce,
				apiKeyRecordIdentity(legacyKey.id, legacyKey.userID),
				legacyKey.keyEncryptionVersion,
			)
			secretUnavailable = decryptErr != nil
		}
		if secretUnavailable {
			legacyKeys = append(legacyKeys, legacyKey)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(legacyKeys) == 0 {
		return 0, nil
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = transaction.Rollback() }()

	updatedAt := formatTime(time.Now().UTC())
	for _, legacyKey := range legacyKeys {
		rawKey, err := generateRawKey()
		if err != nil {
			return 0, err
		}
		keyPrefix := rawKey
		if len(keyPrefix) > 8 {
			keyPrefix = keyPrefix[:8]
		}
		ciphertext, nonce, encryptionVersion, err := s.apiKeyCipher.Encrypt(
			rawKey,
			apiKeyRecordIdentity(legacyKey.id, legacyKey.userID),
		)
		if err != nil {
			return 0, err
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE apikeys
			 SET key_hash = ?, key_prefix = ?, key_ciphertext = ?, key_nonce = ?, key_encryption_version = ?, updated_at = ?
			 WHERE id = ?`,
			keyhash.HashAPIKey(rawKey), keyPrefix, ciphertext, nonce, encryptionVersion, updatedAt, legacyKey.id,
		); err != nil {
			return 0, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return 0, err
	}
	return len(legacyKeys), nil
}

// GetKeyByHash 供鉴权使用；未找到时返回 (nil, nil)。
func (s *SQLiteStore) GetKeyByHash(ctx context.Context, hash string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM apikeys WHERE key_hash = ?`, hash)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (s *SQLiteStore) ListKeysByUser(ctx context.Context, userID string) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+keyColumns+` FROM apikeys WHERE user_id = ? ORDER BY created_at DESC`, userID)
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

func (s *SQLiteStore) ListKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyColumns+` FROM apikeys ORDER BY created_at DESC`)
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
	row := s.db.QueryRowContext(ctx,
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
	return nil
}

func (s *SQLiteStore) RecordUsage(ctx context.Context, record UsageRecord) error {
	success := 0
	if record.Success {
		success = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_log (key_id, tool_name, timestamp, duration_ms, success, debug_json) VALUES (?, ?, ?, ?, ?, ?)`,
		record.KeyID, record.ToolName, formatTime(record.Timestamp.UTC()), record.DurationMs, success, record.DebugJSON,
	)
	return err
}

// TouchKeyUsage 在 tools/call 后递增 total_calls 并刷新 last_used_at；
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

func (s *SQLiteStore) GetUsageStats(ctx context.Context, keyID string, since time.Time) (*UsageStats, error) {
	return s.queryUsageStats(ctx, usageStatsByKey, []any{keyID}, since)
}

func (s *SQLiteStore) GetUserUsageStats(ctx context.Context, userID string, since time.Time) (*UsageStats, error) {
	return s.queryUsageStats(ctx, usageStatsByUser, []any{userID}, since)
}

func (s *SQLiteStore) GetGlobalStats(ctx context.Context, since time.Time) (*UsageStats, error) {
	return s.queryUsageStats(ctx, usageStatsGlobal, nil, since)
}

const usageTrafficBucketCount = 8

// queryUsageStats 按条件聚合 usage_log，并拉取最近 500 条明细（按时间倒序）。
// 流量桶与最近一分钟调用数均直接由 SQLite 对完整数据集聚合，避免被明细上限截断。
func (s *SQLiteStore) queryUsageStats(ctx context.Context, scope usageStatsScope, whereArgs []any, since time.Time) (*UsageStats, error) {
	where, ok := usageStatsWhere[scope]
	if !ok {
		return nil, fmt.Errorf("invalid usage stats scope")
	}
	stats := &UsageStats{ByTool: make(map[string]int64)}

	queryEnd := time.Now().UTC().Truncate(time.Second)
	sinceUTC := since.UTC().Truncate(time.Second)
	sinceStr := formatTime(sinceUTC)
	args := appendUsageStatsArgs(whereArgs, sinceStr)

	rows, err := s.db.QueryContext(ctx,
		`SELECT tool_name, COUNT(*), COALESCE(SUM(success), 0) FROM usage_log WHERE `+where+` AND timestamp >= ? GROUP BY tool_name`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tool string
		var count int64
		var successCount int64
		if err := rows.Scan(&tool, &count, &successCount); err != nil {
			rows.Close()
			return nil, err
		}
		stats.ByTool[tool] = count
		stats.TotalCalls += count
		stats.SuccessCalls += successCount
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	recRows, err := s.db.QueryContext(ctx,
		`SELECT id, key_id, tool_name, timestamp, duration_ms, success, debug_json FROM usage_log WHERE `+where+` AND timestamp >= ? ORDER BY timestamp DESC LIMIT 500`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer recRows.Close()

	for recRows.Next() {
		var r UsageRecord
		var ts string
		var success int
		if err := recRows.Scan(&r.ID, &r.KeyID, &r.ToolName, &ts, &r.DurationMs, &success, &r.DebugJSON); err != nil {
			return nil, err
		}
		r.Success = success != 0
		r.Timestamp, err = parseTime(ts)
		if err != nil {
			return nil, err
		}
		stats.Records = append(stats.Records, r)
	}
	if err := recRows.Err(); err != nil {
		return nil, err
	}
	if err := recRows.Close(); err != nil {
		return nil, err
	}

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
	err := s.db.QueryRowContext(ctx,
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

	var earliestTimestamp sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(timestamp) FROM usage_log WHERE `+where,
		whereArgs...,
	).Scan(&earliestTimestamp); err != nil {
		return time.Time{}, err
	}
	if !earliestTimestamp.Valid || earliestTimestamp.String == "" {
		return queryEnd.Add(-24 * time.Hour), nil
	}

	parsedTimestamp, err := parseTime(earliestTimestamp.String)
	if err != nil {
		return time.Time{}, err
	}
	parsedTimestamp = parsedTimestamp.UTC().Truncate(time.Second)
	if !parsedTimestamp.Before(queryEnd) {
		return queryEnd.Add(-24 * time.Hour), nil
	}
	return parsedTimestamp, nil
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
	queryArgs := make([]any, 0, 2+len(whereArgs)+2)
	queryArgs = append(queryArgs, rangeStart.Unix(), rangeDurationSeconds)
	queryArgs = append(queryArgs, whereArgs...)
	queryArgs = append(queryArgs, formatTime(rangeStart), formatTime(rangeEnd))

	rows, err := s.db.QueryContext(ctx,
		`WITH bucket_window(start_unix, duration_seconds) AS (VALUES (?, ?))
		 SELECT MIN(7, CAST(((CAST(strftime('%s', usage_log.timestamp) AS INTEGER) - bucket_window.start_unix) * 8) / bucket_window.duration_seconds AS INTEGER)) AS bucket_index,
		        COUNT(*)
		 FROM usage_log
		 CROSS JOIN bucket_window
		 WHERE `+where+` AND timestamp >= ? AND timestamp <= ?
		 GROUP BY bucket_index
		 ORDER BY bucket_index`,
		queryArgs...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var bucketIndex int
		var callCount int64
		if err := rows.Scan(&bucketIndex, &callCount); err != nil {
			return nil, err
		}
		if bucketIndex < 0 || bucketIndex >= len(buckets) {
			return nil, fmt.Errorf("invalid usage traffic bucket index %d", bucketIndex)
		}
		buckets[bucketIndex].Calls = callCount
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buckets, nil
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

func scanServerSettings(row interface {
	Scan(dest ...any) error
}) (*ServerSettings, error) {
	var settings ServerSettings
	var proxyEnabled int
	var registrationMode string
	var debug int
	var createdAt string
	var updatedAt string
	err := row.Scan(
		&settings.ID,
		&settings.CPABaseURL,
		&settings.CPAAPIKey,
		&settings.Model,
		&settings.TimeoutSeconds,
		&settings.ProxyURL,
		&proxyEnabled,
		&registrationMode,
		&debug,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	settings.ProxyEnabled = proxyEnabled != 0
	var normalizeErr error
	settings.RegistrationMode, normalizeErr = NormalizeRegistrationMode(RegistrationMode(registrationMode))
	if normalizeErr != nil {
		return nil, normalizeErr
	}
	settings.Debug = debug != 0
	var parseErr error
	settings.CreatedAt, parseErr = parseTime(createdAt)
	if parseErr != nil {
		return nil, parseErr
	}
	settings.UpdatedAt, parseErr = parseTime(updatedAt)
	if parseErr != nil {
		return nil, parseErr
	}
	return &settings, nil
}

const serverSettingsColumns = `id, cpa_base_url, cpa_api_key, model, timeout_seconds, proxy_url, proxy_enabled, registration_mode, debug, created_at, updated_at`

func (s *SQLiteStore) GetServerSettings(ctx context.Context) (*ServerSettings, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+serverSettingsColumns+` FROM server_settings WHERE id = ?`, serverSettingsID)
	settings, err := scanServerSettings(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return settings, err
}

func (s *SQLiteStore) UpsertServerSettings(ctx context.Context, settings ServerSettings) (*ServerSettings, error) {
	cpaBaseURL := strings.TrimSpace(settings.CPABaseURL)
	cpaAPIKey := strings.TrimSpace(settings.CPAAPIKey)
	model := strings.TrimSpace(settings.Model)
	proxyURL := strings.TrimSpace(settings.ProxyURL)
	if cpaBaseURL == "" {
		return nil, fmt.Errorf("cpa_base_url is required")
	}
	if cpaAPIKey == "" {
		return nil, fmt.Errorf("cpa_api_key is required")
	}
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if settings.TimeoutSeconds <= 0 {
		return nil, fmt.Errorf("timeout_seconds must be positive")
	}
	registrationMode, err := NormalizeRegistrationMode(settings.RegistrationMode)
	if err != nil {
		return nil, err
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
			id, cpa_base_url, cpa_api_key, model, timeout_seconds, proxy_url, proxy_enabled, registration_mode, debug, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			cpa_base_url = excluded.cpa_base_url,
			cpa_api_key = excluded.cpa_api_key,
			model = excluded.model,
			timeout_seconds = excluded.timeout_seconds,
			proxy_url = excluded.proxy_url,
			proxy_enabled = excluded.proxy_enabled,
			registration_mode = excluded.registration_mode,
			debug = excluded.debug,
			updated_at = excluded.updated_at`,
		serverSettingsID,
		cpaBaseURL,
		cpaAPIKey,
		model,
		settings.TimeoutSeconds,
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
