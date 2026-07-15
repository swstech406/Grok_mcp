package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const tierColumns = `id, name, level, rpm, success_limit, created_at, updated_at`

func scanTier(row interface {
	Scan(dest ...any) error
}) (*Tier, error) {
	var t Tier
	var createdAt, updatedAt string
	err := row.Scan(
		&t.ID, &t.Name, &t.Level, &t.RPM, &t.SuccessLimit,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *SQLiteStore) GetTierByID(ctx context.Context, id string) (*Tier, error) {
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+tierColumns+` FROM tiers WHERE id = ?`, id)
	t, err := scanTier(row)
	if err == sql.ErrNoRows {
		return nil, ErrTierNotFound
	}
	return t, err
}

// GetTierByName 未找到时返回 (nil, nil) 以便调用方按需 fallback。
func (s *SQLiteStore) GetTierByName(ctx context.Context, name string) (*Tier, error) {
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+tierColumns+` FROM tiers WHERE name = ? COLLATE NOCASE`, strings.TrimSpace(name))
	t, err := scanTier(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *SQLiteStore) ListTiersPage(ctx context.Context, cursor *TierCursor, limit int) (*TierPage, error) {
	pageLimit := normalizePanelPageLimit(limit)
	query := `SELECT ` + tierColumns + ` FROM tiers`
	queryArgs := make([]any, 0, 7)
	if cursor != nil {
		query += ` WHERE level > ?
			OR (level = ? AND name > ?)
			OR (level = ? AND name = ? AND id > ?)`
		queryArgs = append(queryArgs, cursor.Level, cursor.Level, cursor.Name, cursor.Level, cursor.Name, cursor.ID)
	}
	query += ` ORDER BY level ASC, name ASC, id ASC LIMIT ?`
	queryArgs = append(queryArgs, pageLimit+1)

	rows, err := s.readDB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tiers := make([]*Tier, 0, pageLimit+1)
	for rows.Next() {
		tier, scanErr := scanTier(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		tiers = append(tiers, tier)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	page := &TierPage{}
	if len(tiers) > pageLimit {
		page.HasMore = true
		tiers = tiers[:pageLimit]
	}
	page.Tiers = tiers
	if page.HasMore && len(tiers) > 0 {
		lastTier := tiers[len(tiers)-1]
		page.NextCursor = &TierCursor{Level: lastTier.Level, Name: lastTier.Name, ID: lastTier.ID}
	}
	if err := s.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tiers`).Scan(&page.TotalCount); err != nil {
		return nil, err
	}
	return page, nil
}

func (s *SQLiteStore) CreateTier(ctx context.Context, name string, level, rpm, successLimit int) (*Tier, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("tier name is required")
	}
	if level < 0 {
		return nil, fmt.Errorf("level must be >= 0")
	}
	if rpm < 0 {
		return nil, fmt.Errorf("rpm must be >= 0")
	}
	if successLimit < 0 {
		return nil, fmt.Errorf("success_limit must be >= 0")
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	now := formatTime(nowUTC())
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO tiers (id, name, level, rpm, success_limit, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, level, rpm, successLimit, now, now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrTierNameTaken
		}
		return nil, fmt.Errorf("insert tier: %w", err)
	}
	return s.GetTierByID(ctx, id)
}

func (s *SQLiteStore) UpdateTier(ctx context.Context, id string, updates TierUpdates) (*Tier, error) {
	if _, err := s.GetTierByID(ctx, id); err != nil {
		return nil, err
	}
	var sets []string
	var args []any
	if updates.Name != nil {
		name := strings.TrimSpace(*updates.Name)
		if name == "" {
			return nil, fmt.Errorf("tier name must not be empty")
		}
		sets = append(sets, "name = ?")
		args = append(args, name)
	}
	if updates.Level != nil {
		if *updates.Level < 0 {
			return nil, fmt.Errorf("level must be >= 0")
		}
		sets = append(sets, "level = ?")
		args = append(args, *updates.Level)
	}
	if updates.RPM != nil {
		if *updates.RPM < 0 {
			return nil, fmt.Errorf("rpm must be >= 0")
		}
		sets = append(sets, "rpm = ?")
		args = append(args, *updates.RPM)
	}
	if updates.SuccessLimit != nil {
		if *updates.SuccessLimit < 0 {
			return nil, fmt.Errorf("success_limit must be >= 0")
		}
		sets = append(sets, "success_limit = ?")
		args = append(args, *updates.SuccessLimit)
	}
	if len(sets) == 0 {
		return s.GetTierByID(ctx, id)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, formatTime(nowUTC()))
	args = append(args, id)
	q := `UPDATE tiers SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrTierNameTaken
		}
		return nil, err
	}
	return s.GetTierByID(ctx, id)
}

func (s *SQLiteStore) DeleteTier(ctx context.Context, id string) error {
	var n int64
	if err := s.db.QueryRowContext(ctx, countUsersByTierQuery, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrTierInUse
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM tiers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrTierNotFound
	}
	return nil
}

const countUsersByTierQuery = `SELECT COUNT(*) FROM users WHERE tier_id = ?`

func (s *SQLiteStore) CountUsersByTier(ctx context.Context, tierID string) (int64, error) {
	var n int64
	err := s.readDB.QueryRowContext(ctx, countUsersByTierQuery, tierID).Scan(&n)
	return n, err
}
