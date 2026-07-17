package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/keycrypt"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/keyhash"
)

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

// generateRawKey 生成 grok_<64 hex> 形态的客户端密钥明文。
func generateRawKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "grok_" + hex.EncodeToString(b), nil
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

func (s *SQLiteStore) ListKeysByUserPage(ctx context.Context, userID string, cursor *TimeIDCursor, limit int) (*APIKeyPage, error) {
	pageLimit := normalizePanelPageLimit(limit)
	query := `SELECT ` + keyColumns + ` FROM apikeys WHERE user_id = ?`
	queryArgs := []any{userID}
	if cursor != nil {
		query += ` AND ` + timeIDCursorPredicate(timeIDDescending)
		queryArgs = appendTimeIDCursorArguments(queryArgs, cursor)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	queryArgs = append(queryArgs, keysetFetchLimit(pageLimit))

	rows, err := s.readDB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := make([]*APIKey, 0, keysetFetchLimit(pageLimit))
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

	keys, hasMore, nextCursor := finalizeTimeIDPage(keys, pageLimit, func(apiKey *APIKey) TimeIDCursor {
		return TimeIDCursor{Timestamp: apiKey.CreatedAt, ID: apiKey.ID}
	})
	page := &APIKeyPage{
		Keys:       keys,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END), 0) FROM apikeys WHERE user_id = ?`,
		userID,
	).Scan(&page.TotalCount, &page.ActiveCount); err != nil {
		return nil, err
	}
	return page, nil
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
