package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const userColumns = `id, username, password_hash, role, enabled, tier_id, success_calls, token_version, created_at, updated_at`

func scanUser(row interface {
	Scan(dest ...any) error
}) (*User, error) {
	var u User
	var role string
	var enabled int
	var tierID sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &role, &enabled, &tierID,
		&u.SuccessCalls,
		&u.TokenVersion, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	u.Role = UserRole(role)
	u.Enabled = enabled != 0
	if tierID.Valid {
		u.TierID = tierID.String
	}
	u.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	u.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser 插入新用户；用户名冲突返回 ErrUsernameTaken。限额由默认 tier0 决定，不再随用户保存。
func (s *SQLiteStore) CreateUser(ctx context.Context, username, passwordHash string, role UserRole) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	if role != RoleAdmin && role != RoleUser {
		return nil, fmt.Errorf("invalid role")
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	now := formatTime(nowUTC())
	tierID, _ := s.defaultTierID(ctx)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, tier_id, success_calls, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, 0, ?, ?)`,
		id, username, passwordHash, string(role), nullableString(tierID), now, now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return s.GetUserByID(ctx, id)
}

// RegisterUser 插入自助注册用户；自助注册始终创建普通用户，启动 bootstrap 负责创建管理员。
// 限额由默认 tier0 决定，不再随用户保存。
func (s *SQLiteStore) RegisterUser(ctx context.Context, username, passwordHash string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	id, err := randomID()
	if err != nil {
		return nil, err
	}
	now := formatTime(nowUTC())
	var tierID sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT id FROM tiers WHERE name = 'tier0' LIMIT 1`).Scan(&tierID)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, tier_id, success_calls, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, 0, ?, ?)`,
		id, username, passwordHash, string(RoleUser), tierID, now, now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUserByID(ctx, id)
}

func (s *SQLiteStore) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ? COLLATE NOCASE`, strings.TrimSpace(username))
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (s *SQLiteStore) GetUserByID(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	return u, err
}

func (s *SQLiteStore) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpdateUser(ctx context.Context, id string, updates UserUpdates) (*User, error) {
	if _, err := s.GetUserByID(ctx, id); err != nil {
		return nil, err
	}
	var sets []string
	var args []any
	// 角色、启用状态变更或显式吊销均会自增 token_version，使存量 JWT 立即失效。
	bumpTokenVersion := false
	if updates.Enabled != nil {
		en := 0
		if *updates.Enabled {
			en = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, en)
		bumpTokenVersion = true
	}
	if updates.Role != nil {
		if *updates.Role != RoleAdmin && *updates.Role != RoleUser {
			return nil, fmt.Errorf("invalid role")
		}
		sets = append(sets, "role = ?")
		args = append(args, string(*updates.Role))
		bumpTokenVersion = true
	}
	if updates.TierID != nil {
		sets = append(sets, "tier_id = ?")
		args = append(args, nullableString(*updates.TierID))
	}
	if updates.RevokeTokens != nil && *updates.RevokeTokens {
		bumpTokenVersion = true
	}
	if bumpTokenVersion {
		sets = append(sets, "token_version = token_version + 1")
	}
	if len(sets) == 0 {
		return s.GetUserByID(ctx, id)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, formatTime(nowUTC()))
	args = append(args, id)
	q := `UPDATE users SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return nil, err
	}
	return s.GetUserByID(ctx, id)
}

func (s *SQLiteStore) DeleteUser(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, id).Scan(&role); err != nil {
		if err == sql.ErrNoRows {
			return ErrUserNotFound
		}
		return err
	}

	if UserRole(role) == RoleAdmin {
		var adminCount int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = ?`, string(RoleAdmin)).Scan(&adminCount); err != nil {
			return err
		}
		if adminCount <= 1 {
			return ErrLastAdmin
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_log WHERE key_id IN (SELECT id FROM apikeys WHERE user_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM apikeys WHERE user_id = ?`, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return ErrUserNotFound
	}
	return tx.Commit()
}

func (s *SQLiteStore) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *SQLiteStore) CountEnabledAdmins(ctx context.Context) (int64, error) {
	var enabledAdminCount int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = ? AND enabled = 1`, string(RoleAdmin),
	).Scan(&enabledAdminCount)
	return enabledAdminCount, err
}

// ReserveSuccessCall 在 tools/call 前原子递增 success_calls；success_limit 为 0 表示不限。
func (s *SQLiteStore) ReserveSuccessCall(ctx context.Context, userID string, successLimit int) error {
	return s.TryIncrementUserSuccessCalls(ctx, userID, successLimit)
}

// ReleaseSuccessCall 在 MCP 工具返回 IsError 或 HTTP 非 2xx 时回滚 ReserveSuccessCall。
func (s *SQLiteStore) ReleaseSuccessCall(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET success_calls = success_calls - 1 WHERE id = ? AND success_calls > 0`, userID)
	return err
}

// TryIncrementUserSuccessCalls 仅在未达 success_limit 时递增；success_limit 为 0 表示不限。
func (s *SQLiteStore) TryIncrementUserSuccessCalls(ctx context.Context, userID string, successLimit int) error {
	var res sql.Result
	var err error
	if successLimit <= 0 {
		res, err = s.db.ExecContext(ctx,
			`UPDATE users SET success_calls = success_calls + 1 WHERE id = ?`, userID)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE users SET success_calls = success_calls + 1 WHERE id = ? AND success_calls < ?`,
			userID, successLimit)
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrQuotaSuccess
	}
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

// defaultTierID 返回默认 tier0 的 ID；若 tier 表尚未初始化则返回空串。
func (s *SQLiteStore) defaultTierID(ctx context.Context) (string, error) {
	var id sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id FROM tiers WHERE name = 'tier0' LIMIT 1`).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if id.Valid {
		return id.String, nil
	}
	return "", nil
}

// nullableString 将空串转为 sql.NullString（NULL），非空串保留。
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
