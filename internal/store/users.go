package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const userColumns = `id, username, password_hash, role, enabled, tier_id, success_calls, success_period, token_version, created_at, updated_at`

const successQuotaPeriodLayout = "2006-01"

type successQuotaNowContextKey struct{}

// WithSuccessQuotaNow pins the quota clock for tests that need to cross month boundaries.
func WithSuccessQuotaNow(ctx context.Context, now time.Time) context.Context {
	return context.WithValue(ctx, successQuotaNowContextKey{}, now.UTC())
}

func successQuotaNow(ctx context.Context) time.Time {
	if ctx == nil {
		return nowUTC()
	}
	if now, ok := ctx.Value(successQuotaNowContextKey{}).(time.Time); ok && !now.IsZero() {
		return now.UTC()
	}
	return nowUTC()
}

func successQuotaPeriod(ctx context.Context) string {
	return successQuotaNow(ctx).Format(successQuotaPeriodLayout)
}

type queryRowContextExecutor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanUser(row interface {
	Scan(dest ...any) error
}) (*User, error) {
	var u User
	var role string
	var enabled int
	var successPeriod sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &role, &enabled, &u.TierID,
		&u.SuccessCalls, &successPeriod,
		&u.TokenVersion, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	u.Role = UserRole(role)
	u.Enabled = enabled != 0
	if successPeriod.Valid {
		u.SuccessPeriod = successPeriod.String
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
	tierID, err := s.defaultTierID(ctx)
	if err != nil {
		return nil, err
	}
	period := successQuotaPeriod(ctx)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, tier_id, success_calls, success_period, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, 0, ?, ?, ?)`,
		id, username, passwordHash, string(role), tierID, period, now, now,
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
	period := successQuotaPeriod(ctx)
	var tierID string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM tiers WHERE name = ? COLLATE NOCASE LIMIT 1`, DefaultTierName,
	).Scan(&tierID); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrTierNotFound
		}
		return nil, err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, tier_id, success_calls, success_period, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, 0, ?, ?, ?)`,
		id, username, passwordHash, string(RoleUser), tierID, period, now, now,
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
	if err == nil {
		err = s.resetUserSuccessPeriodIfNeeded(ctx, u)
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
	if err == nil {
		err = s.resetUserSuccessPeriodIfNeeded(ctx, u)
	}
	return u, err
}

func (s *SQLiteStore) ListUsersPage(ctx context.Context, cursor *TimeIDCursor, limit int) (*UserPage, error) {
	pageLimit := normalizePanelPageLimit(limit)
	query := `SELECT ` + userColumns + ` FROM users`
	queryArgs := make([]any, 0, 4)
	if cursor != nil {
		cursorTimestamp := formatTime(cursor.Timestamp.UTC())
		query += ` WHERE created_at > ? OR (created_at = ? AND id > ?)`
		queryArgs = append(queryArgs, cursorTimestamp, cursorTimestamp, cursor.ID)
	}
	query += ` ORDER BY created_at ASC, id ASC LIMIT ?`
	queryArgs = append(queryArgs, pageLimit+1)

	rows, err := s.readDB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]*User, 0, pageLimit+1)
	for rows.Next() {
		user, scanErr := scanUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	page := &UserPage{}
	if len(users) > pageLimit {
		page.HasMore = true
		users = users[:pageLimit]
	}
	for _, user := range users {
		if err := s.resetUserSuccessPeriodIfNeeded(ctx, user); err != nil {
			return nil, err
		}
	}
	page.Users = users
	if page.HasMore && len(users) > 0 {
		lastUser := users[len(users)-1]
		page.NextCursor = &TimeIDCursor{Timestamp: lastUser.CreatedAt, ID: lastUser.ID}
	}
	if err := s.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&page.TotalCount); err != nil {
		return nil, err
	}
	return page, nil
}

func (s *SQLiteStore) UpdateUser(ctx context.Context, id string, updates UserUpdates) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	existingUser, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	var sets []string
	var args []any
	// 角色、启用状态变更或显式吊销均会自增 token_version，使存量 JWT 立即失效。
	bumpTokenVersion := false
	updatedRole := existingUser.Role
	updatedEnabled := existingUser.Enabled
	if updates.Enabled != nil {
		if *updates.Enabled != existingUser.Enabled {
			en := 0
			if *updates.Enabled {
				en = 1
			}
			sets = append(sets, "enabled = ?")
			args = append(args, en)
			bumpTokenVersion = true
			updatedEnabled = *updates.Enabled
		}
	}
	if updates.Role != nil {
		if *updates.Role != RoleAdmin && *updates.Role != RoleUser {
			return nil, fmt.Errorf("invalid role")
		}
		if *updates.Role != existingUser.Role {
			sets = append(sets, "role = ?")
			args = append(args, string(*updates.Role))
			bumpTokenVersion = true
			updatedRole = *updates.Role
		}
	}
	if updates.TierID != nil {
		tierID := strings.TrimSpace(*updates.TierID)
		if tierID == "" {
			return nil, ErrTierNotAssignable
		}
		if err := validateAssignableTierID(ctx, tx, tierID); err != nil {
			return nil, err
		}
		sets = append(sets, "tier_id = ?")
		args = append(args, tierID)
	}
	if updates.PasswordHash != nil {
		passwordHash := strings.TrimSpace(*updates.PasswordHash)
		if passwordHash == "" {
			return nil, fmt.Errorf("password_hash must not be empty")
		}
		sets = append(sets, "password_hash = ?")
		args = append(args, passwordHash)
		bumpTokenVersion = true
	}
	if updates.RevokeTokens != nil && *updates.RevokeTokens {
		bumpTokenVersion = true
	}
	if bumpTokenVersion {
		sets = append(sets, "token_version = token_version + 1")
	}
	if len(sets) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		if err := s.resetUserSuccessPeriodIfNeeded(ctx, existingUser); err != nil {
			return nil, err
		}
		return existingUser, nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, formatTime(nowUTC()))
	args = append(args, id)
	q := `UPDATE users SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	result, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, ErrUserNotFound
	}

	removesEnabledAdmin := existingUser.Role == RoleAdmin && existingUser.Enabled && (updatedRole != RoleAdmin || !updatedEnabled)
	if removesEnabledAdmin {
		var enabledAdminCount int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users WHERE role = ? AND enabled = 1`, string(RoleAdmin),
		).Scan(&enabledAdminCount); err != nil {
			return nil, err
		}
		if enabledAdminCount < 1 {
			return nil, ErrLastAdmin
		}
	}

	updatedUser, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := s.resetUserSuccessPeriodIfNeeded(ctx, updatedUser); err != nil {
		return nil, err
	}
	return updatedUser, nil
}

func (s *SQLiteStore) DeleteUser(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var role string
	var enabled int
	if err := tx.QueryRowContext(ctx, `SELECT role, enabled FROM users WHERE id = ?`, id).Scan(&role, &enabled); err != nil {
		if err == sql.ErrNoRows {
			return ErrUserNotFound
		}
		return err
	}

	if UserRole(role) == RoleAdmin && enabled != 0 {
		var enabledAdminCount int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users WHERE role = ? AND enabled = 1`, string(RoleAdmin),
		).Scan(&enabledAdminCount); err != nil {
			return err
		}
		if enabledAdminCount <= 1 {
			return ErrLastAdmin
		}
	}

	keyRows, err := tx.QueryContext(ctx, `SELECT id FROM apikeys WHERE user_id = ?`, id)
	if err != nil {
		return err
	}
	keyIDs := make([]string, 0)
	for keyRows.Next() {
		var keyID string
		if err := keyRows.Scan(&keyID); err != nil {
			_ = keyRows.Close()
			return err
		}
		keyIDs = append(keyIDs, keyID)
	}
	if err := keyRows.Close(); err != nil {
		return err
	}
	if err := keyRows.Err(); err != nil {
		return err
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
	if err := tx.Commit(); err != nil {
		return err
	}
	s.deleteUsageDebugByKeyIDsBestEffort(keyIDs)
	return nil
}

func (s *SQLiteStore) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *SQLiteStore) CountEnabledAdmins(ctx context.Context) (int64, error) {
	var enabledAdminCount int64
	err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = ? AND enabled = 1`, string(RoleAdmin),
	).Scan(&enabledAdminCount)
	return enabledAdminCount, err
}

// ReserveSuccessCall 在 tools/call 前原子递增当月 success_calls；success_limit 为 0 表示不限。
func (s *SQLiteStore) ReserveSuccessCall(ctx context.Context, userID string, successLimit int) error {
	return s.TryIncrementUserSuccessCalls(ctx, userID, successLimit)
}

// ReleaseSuccessCall 在 MCP 工具返回 IsError 或 HTTP 非 2xx 时回滚 ReserveSuccessCall。
func (s *SQLiteStore) ReleaseSuccessCall(ctx context.Context, userID string) error {
	period := successQuotaPeriod(ctx)
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET success_calls = success_calls - 1 WHERE id = ? AND success_period = ? AND success_calls > 0`,
		userID, period,
	)
	return err
}

// TryIncrementUserSuccessCalls 仅在未达当月 success_limit 时递增；success_limit 为 0 表示不限。
// RowsAffected==0 时区分用户不存在（ErrUserNotFound）与额度耗尽（ErrQuotaSuccess）。
func (s *SQLiteStore) TryIncrementUserSuccessCalls(ctx context.Context, userID string, successLimit int) error {
	period := successQuotaPeriod(ctx)
	res, err := s.db.ExecContext(ctx,
		`UPDATE users
		 SET success_calls = CASE WHEN success_period = ? THEN success_calls + 1 ELSE 1 END,
		     success_period = ?
		 WHERE id = ?
		   AND (? <= 0 OR CASE WHEN success_period = ? THEN success_calls ELSE 0 END < ?)`,
		period, period, userID, successLimit, period, successLimit,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var exists int
		lookupErr := s.db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&exists)
		if lookupErr == sql.ErrNoRows {
			return ErrUserNotFound
		}
		if lookupErr != nil {
			return lookupErr
		}
		return ErrQuotaSuccess
	}
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

// defaultTierID 返回默认 tier0 的 ID；若 tier 表尚未初始化则 fail-closed。
func (s *SQLiteStore) defaultTierID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM tiers WHERE name = ? COLLATE NOCASE LIMIT 1`, DefaultTierName,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", ErrTierNotFound
	}
	return id, err
}

func (s *SQLiteStore) resetUserSuccessPeriodIfNeeded(ctx context.Context, user *User) error {
	if user == nil {
		return nil
	}
	period := successQuotaPeriod(ctx)
	if user.SuccessPeriod == period {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET success_calls = 0, success_period = ? WHERE id = ? AND COALESCE(success_period, '') <> ?`,
		period, user.ID, period,
	)
	if err != nil {
		return err
	}
	user.SuccessCalls = 0
	user.SuccessPeriod = period
	return nil
}

func validateAssignableTierID(ctx context.Context, executor queryRowContextExecutor, tierID string) error {
	var tierName string
	if err := executor.QueryRowContext(ctx,
		`SELECT name FROM tiers WHERE id = ?`, strings.TrimSpace(tierID),
	).Scan(&tierName); err != nil {
		if err == sql.ErrNoRows {
			return ErrTierNotAssignable
		}
		return err
	}
	return nil
}
