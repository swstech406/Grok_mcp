package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const userColumns = `id, username, password_hash, role, enabled, rpm, total_limit, success_limit, total_calls, success_calls, created_at, updated_at`

func scanUser(row interface {
	Scan(dest ...any) error
}) (*User, error) {
	var u User
	var role string
	var enabled int
	var createdAt, updatedAt string
	err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &role, &enabled,
		&u.RPM, &u.TotalLimit, &u.SuccessLimit, &u.TotalCalls, &u.SuccessCalls,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	u.Role = UserRole(role)
	u.Enabled = enabled != 0
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

// CreateUser 插入新用户；用户名冲突返回 ErrUsernameTaken。
func (s *SQLiteStore) CreateUser(ctx context.Context, username, passwordHash string, role UserRole, rpm, totalLimit, successLimit int) (*User, error) {
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
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, rpm, total_limit, success_limit, total_calls, success_calls, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?, 0, 0, ?, ?)`,
		id, username, passwordHash, string(role), rpm, totalLimit, successLimit, now, now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return s.GetUserByID(ctx, id)
}

// RegisterUser 插入新用户；若当前无用户则 role=admin，否则 role=user。全程在同一事务内完成。
func (s *SQLiteStore) RegisterUser(ctx context.Context, username, passwordHash string, rpm, totalLimit, successLimit int) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return nil, err
	}
	role := RoleUser
	if count == 0 {
		role = RoleAdmin
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	now := formatTime(nowUTC())
	_, err = tx.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, rpm, total_limit, success_limit, total_calls, success_calls, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?, 0, 0, ?, ?)`,
		id, username, passwordHash, string(role), rpm, totalLimit, successLimit, now, now,
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
	if updates.Enabled != nil {
		en := 0
		if *updates.Enabled {
			en = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, en)
	}
	if updates.Role != nil {
		if *updates.Role != RoleAdmin && *updates.Role != RoleUser {
			return nil, fmt.Errorf("invalid role")
		}
		sets = append(sets, "role = ?")
		args = append(args, string(*updates.Role))
	}
	if updates.RPM != nil {
		if *updates.RPM < 0 {
			return nil, fmt.Errorf("rpm must be >= 0")
		}
		sets = append(sets, "rpm = ?")
		args = append(args, *updates.RPM)
	}
	if updates.TotalLimit != nil {
		if *updates.TotalLimit < 0 {
			return nil, fmt.Errorf("total_limit must be >= 0")
		}
		sets = append(sets, "total_limit = ?")
		args = append(args, *updates.TotalLimit)
	}
	if updates.SuccessLimit != nil {
		if *updates.SuccessLimit < 0 {
			return nil, fmt.Errorf("success_limit must be >= 0")
		}
		sets = append(sets, "success_limit = ?")
		args = append(args, *updates.SuccessLimit)
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

func (s *SQLiteStore) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ReserveTotalCall 在 tools/call 前原子递增用户 total_calls；total_limit 为 0 表示不限。
func (s *SQLiteStore) ReserveTotalCall(ctx context.Context, userID string, totalLimit int) error {
	var res sql.Result
	var err error
	if totalLimit <= 0 {
		res, err = s.db.ExecContext(ctx,
			`UPDATE users SET total_calls = total_calls + 1 WHERE id = ?`, userID)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE users SET total_calls = total_calls + 1 WHERE id = ? AND total_calls < ?`,
			userID, totalLimit)
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrQuotaTotal
	}
	return nil
}

// ReleaseTotalCall 在 tools/call 未实际完成时回滚 ReserveTotalCall 预留的总次数（success_calls 不变）。
func (s *SQLiteStore) ReleaseTotalCall(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET total_calls = total_calls - 1 WHERE id = ? AND total_calls > 0`, userID)
	return err
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

// CheckUserSuccessQuota 读取最新用户状态，判断成功额度是否已耗尽（用于 tools/call 前拒绝）。
func (s *SQLiteStore) CheckUserSuccessQuota(ctx context.Context, user *User) error {
	if user == nil {
		return fmt.Errorf("user is nil")
	}
	fresh, err := s.GetUserByID(ctx, user.ID)
	if err != nil {
		return err
	}
	if fresh.SuccessLimit > 0 && fresh.SuccessCalls >= int64(fresh.SuccessLimit) {
		return ErrQuotaSuccess
	}
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}