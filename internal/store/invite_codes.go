package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/grok-mcp/internal/keyhash"
)

const inviteCodeColumns = `id, code, code_hash, code_prefix, registration_limit, registration_count, enabled, created_by_user_id, created_at, updated_at`

func scanInviteCode(row interface {
	Scan(dest ...any) error
}) (*InviteCode, error) {
	var inviteCode InviteCode
	var enabled int
	var createdByUserID sql.NullString
	var createdAt string
	var updatedAt string

	err := row.Scan(
		&inviteCode.ID,
		&inviteCode.Code,
		&inviteCode.CodeHash,
		&inviteCode.CodePrefix,
		&inviteCode.RegistrationLimit,
		&inviteCode.RegistrationCount,
		&enabled,
		&createdByUserID,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}

	inviteCode.Enabled = enabled != 0
	if createdByUserID.Valid {
		inviteCode.CreatedByUserID = createdByUserID.String
	}
	var parseErr error
	inviteCode.CreatedAt, parseErr = parseTime(createdAt)
	if parseErr != nil {
		return nil, parseErr
	}
	inviteCode.UpdatedAt, parseErr = parseTime(updatedAt)
	if parseErr != nil {
		return nil, parseErr
	}
	return &inviteCode, nil
}

func (s *SQLiteStore) ListInviteCodesPage(ctx context.Context, cursor *TimeIDCursor, limit int) (*InviteCodePage, error) {
	pageLimit := normalizePanelPageLimit(limit)
	query := `SELECT ` + inviteCodeColumns + ` FROM invite_codes`
	queryArgs := make([]any, 0, 4)
	if cursor != nil {
		cursorTimestamp := formatTime(cursor.Timestamp.UTC())
		query += ` WHERE created_at < ? OR (created_at = ? AND id < ?)`
		queryArgs = append(queryArgs, cursorTimestamp, cursorTimestamp, cursor.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	queryArgs = append(queryArgs, pageLimit+1)

	rows, err := s.readDB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	inviteCodes := make([]*InviteCode, 0, pageLimit+1)
	for rows.Next() {
		inviteCode, scanErr := scanInviteCode(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		inviteCodes = append(inviteCodes, inviteCode)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	page := &InviteCodePage{}
	if len(inviteCodes) > pageLimit {
		page.HasMore = true
		inviteCodes = inviteCodes[:pageLimit]
	}
	page.InviteCodes = inviteCodes
	if page.HasMore && len(inviteCodes) > 0 {
		lastInviteCode := inviteCodes[len(inviteCodes)-1]
		page.NextCursor = &TimeIDCursor{Timestamp: lastInviteCode.CreatedAt, ID: lastInviteCode.ID}
	}
	if err := s.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM invite_codes`).Scan(&page.TotalCount); err != nil {
		return nil, err
	}
	return page, nil
}

func (s *SQLiteStore) CreateInviteCode(ctx context.Context, createdByUserID string, registrationLimit int) (*InviteCode, string, error) {
	createdByUserID = strings.TrimSpace(createdByUserID)
	if registrationLimit <= 0 {
		return nil, "", fmt.Errorf("registration_limit must be positive")
	}

	rawInviteCode, err := generateRawKey()
	if err != nil {
		return nil, "", err
	}
	inviteCodeID, err := randomID()
	if err != nil {
		return nil, "", err
	}

	codePrefix := rawInviteCode
	if len(codePrefix) > 12 {
		codePrefix = codePrefix[:12]
	}
	now := formatTime(time.Now().UTC())

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO invite_codes (id, code, code_hash, code_prefix, registration_limit, registration_count, enabled, created_by_user_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 0, 1, ?, ?, ?)`,
		inviteCodeID,
		rawInviteCode,
		keyhash.HashAPIKey(rawInviteCode),
		codePrefix,
		registrationLimit,
		createdByUserID,
		now,
		now,
	)
	if err != nil {
		return nil, "", fmt.Errorf("insert invite code: %w", err)
	}

	inviteCode, err := s.getInviteCodeByID(ctx, inviteCodeID)
	if err != nil {
		return nil, "", err
	}
	return inviteCode, rawInviteCode, nil
}

func (s *SQLiteStore) UpdateInviteCode(ctx context.Context, id string, updates InviteCodeUpdates) (*InviteCode, error) {
	existingInviteCode, err := s.getInviteCodeByID(ctx, id)
	if err != nil {
		return nil, err
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if updates.RegistrationLimit != nil {
		registrationLimit := *updates.RegistrationLimit
		if registrationLimit <= 0 {
			return nil, fmt.Errorf("registration_limit must be positive")
		}
		if registrationLimit < existingInviteCode.RegistrationCount {
			return nil, ErrInviteCodeLimitTooLow
		}
		sets = append(sets, "registration_limit = ?")
		args = append(args, registrationLimit)
	}
	if updates.Enabled != nil {
		enabled := 0
		if *updates.Enabled {
			enabled = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, enabled)
	}

	if len(sets) == 0 {
		return existingInviteCode, nil
	}

	sets = append(sets, "updated_at = ?")
	args = append(args, formatTime(time.Now().UTC()))
	args = append(args, strings.TrimSpace(id))

	query := `UPDATE invite_codes SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, ErrInviteCodeNotFound
	}
	return s.getInviteCodeByID(ctx, id)
}

func (s *SQLiteStore) DeleteInviteCode(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM invite_codes WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return ErrInviteCodeNotFound
	}
	return nil
}

func (s *SQLiteStore) RegisterUserWithInviteCode(ctx context.Context, username, passwordHash, rawInviteCode string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	rawInviteCode = strings.TrimSpace(rawInviteCode)
	if rawInviteCode == "" {
		return nil, ErrInviteCodeInvalid
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = transaction.Rollback() }()

	inviteCode, err := scanInviteCode(transaction.QueryRowContext(ctx,
		`SELECT `+inviteCodeColumns+` FROM invite_codes WHERE code_hash = ?`, keyhash.HashAPIKey(rawInviteCode),
	))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrInviteCodeInvalid
		}
		return nil, err
	}
	if !inviteCode.Enabled {
		return nil, ErrInviteCodeDisabled
	}
	if inviteCode.RegistrationCount >= inviteCode.RegistrationLimit {
		return nil, ErrInviteCodeExhausted
	}

	var tierID string
	if err := transaction.QueryRowContext(ctx,
		`SELECT id FROM tiers WHERE name = ? COLLATE NOCASE LIMIT 1`, DefaultTierName,
	).Scan(&tierID); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrTierNotFound
		}
		return nil, err
	}

	userID, err := randomID()
	if err != nil {
		return nil, err
	}
	now := formatTime(nowUTC())
	period := successQuotaPeriod(ctx)
	_, err = transaction.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, tier_id, success_calls, success_period, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, 0, ?, ?, ?)`,
		userID,
		username,
		passwordHash,
		string(RoleUser),
		tierID,
		period,
		now,
		now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}

	inviteCodeUpdateResult, err := transaction.ExecContext(ctx,
		`UPDATE invite_codes
		 SET registration_count = registration_count + 1, updated_at = ?
		 WHERE id = ? AND enabled = 1 AND registration_count < registration_limit`,
		now,
		inviteCode.ID,
	)
	if err != nil {
		return nil, err
	}
	updatedInviteCodeRows, err := inviteCodeUpdateResult.RowsAffected()
	if err != nil {
		return nil, err
	}
	if updatedInviteCodeRows != 1 {
		return nil, ErrInviteCodeExhausted
	}

	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return s.GetUserByID(ctx, userID)
}

func (s *SQLiteStore) getInviteCodeByID(ctx context.Context, id string) (*InviteCode, error) {
	inviteCode, err := scanInviteCode(s.readDB.QueryRowContext(ctx,
		`SELECT `+inviteCodeColumns+` FROM invite_codes WHERE id = ?`, strings.TrimSpace(id),
	))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrInviteCodeNotFound
		}
		return nil, err
	}
	return inviteCode, nil
}
