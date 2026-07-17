package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/keyhash"
)

// ErrRegistrationDisabled indicates that public registration is disabled at
// the point where the registration transaction acquired its database lock.
var ErrRegistrationDisabled = errors.New("registration is disabled")

// RegisterUserWithCurrentMode registers a user according to the registration
// mode observed in the same transaction as the user insert. fallbackMode is
// used only when server_settings has not been persisted yet (for example,
// during first boot).
func (s *SQLiteStore) RegisterUserWithCurrentMode(
	ctx context.Context,
	username string,
	passwordHash string,
	rawInviteCode string,
	fallbackMode RegistrationMode,
) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}

	fallbackMode, err := NormalizeRegistrationMode(fallbackMode)
	if err != nil {
		return nil, err
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = transaction.Rollback() }()

	registrationMode, err := readRegistrationMode(ctx, transaction, fallbackMode)
	if err != nil {
		return nil, err
	}

	var userID string
	switch registrationMode {
	case RegistrationModeDisabled:
		return nil, ErrRegistrationDisabled
	case RegistrationModeFree:
		userID, err = registerUserInTransaction(ctx, transaction, username, passwordHash)
	case RegistrationModeInvite:
		userID, err = registerUserWithInviteCodeInTransaction(ctx, transaction, username, passwordHash, rawInviteCode)
	default:
		return nil, fmt.Errorf("unsupported registration mode %q", registrationMode)
	}
	if err != nil {
		return nil, err
	}

	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return s.GetUserByID(ctx, userID)
}

func readRegistrationMode(ctx context.Context, transaction *sql.Tx, fallbackMode RegistrationMode) (RegistrationMode, error) {
	var persistedMode string
	err := transaction.QueryRowContext(
		ctx,
		`SELECT registration_mode FROM server_settings WHERE id = ?`,
		serverSettingsID,
	).Scan(&persistedMode)
	if err == sql.ErrNoRows {
		return fallbackMode, nil
	}
	if err != nil {
		return "", err
	}
	return NormalizeRegistrationMode(RegistrationMode(persistedMode))
}

func registerUserInTransaction(ctx context.Context, transaction *sql.Tx, username, passwordHash string) (string, error) {
	userID, err := randomID()
	if err != nil {
		return "", err
	}
	now := formatTime(nowUTC())
	period := successQuotaPeriod(ctx)
	var tierID string
	if err := transaction.QueryRowContext(ctx,
		`SELECT id FROM tiers WHERE name = ? COLLATE NOCASE LIMIT 1`, DefaultTierName,
	).Scan(&tierID); err != nil {
		if err == sql.ErrNoRows {
			return "", ErrTierNotFound
		}
		return "", err
	}

	_, err = transaction.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, tier_id, success_calls, success_period, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, 0, ?, ?, ?)`,
		userID, username, passwordHash, string(RoleUser), tierID, period, now, now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", ErrUsernameTaken
		}
		return "", fmt.Errorf("insert user: %w", err)
	}
	return userID, nil
}

func registerUserWithInviteCodeInTransaction(
	ctx context.Context,
	transaction *sql.Tx,
	username string,
	passwordHash string,
	rawInviteCode string,
) (string, error) {
	rawInviteCode = strings.TrimSpace(rawInviteCode)
	if rawInviteCode == "" {
		return "", ErrInviteCodeInvalid
	}

	inviteCode, err := scanInviteCode(transaction.QueryRowContext(ctx,
		`SELECT `+inviteCodeColumns+` FROM invite_codes WHERE code_hash = ?`, keyhash.HashAPIKey(rawInviteCode),
	))
	if err != nil {
		if err == sql.ErrNoRows {
			return "", ErrInviteCodeInvalid
		}
		return "", err
	}
	if !inviteCode.Enabled {
		return "", ErrInviteCodeDisabled
	}
	if inviteCode.RegistrationCount >= inviteCode.RegistrationLimit {
		return "", ErrInviteCodeExhausted
	}

	userID, err := registerUserInTransaction(ctx, transaction, username, passwordHash)
	if err != nil {
		return "", err
	}
	now := formatTime(nowUTC())
	inviteCodeUpdateResult, err := transaction.ExecContext(ctx,
		`UPDATE invite_codes
		 SET registration_count = registration_count + 1, updated_at = ?
		 WHERE id = ? AND enabled = 1 AND registration_count < registration_limit`,
		now,
		inviteCode.ID,
	)
	if err != nil {
		return "", err
	}
	updatedInviteCodeRows, err := inviteCodeUpdateResult.RowsAffected()
	if err != nil {
		return "", err
	}
	if updatedInviteCodeRows != 1 {
		return "", ErrInviteCodeExhausted
	}
	return userID, nil
}
