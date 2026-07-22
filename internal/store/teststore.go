package store

import (
	"context"
	"time"
)

// TestStore 为单元测试提供可嵌入的 Store 桩，未覆盖的方法返回零值。
type TestStore struct {
	Store
}

func (TestStore) Close() error { return nil }

func (TestStore) CreateUser(context.Context, string, string, UserRole) (*User, error) {
	return nil, nil
}
func (TestStore) RegisterUser(context.Context, string, string) (*User, error) {
	return nil, nil
}
func (TestStore) RegisterUserWithInviteCode(context.Context, string, string, string) (*User, error) {
	return nil, nil
}
func (TestStore) RegisterUserWithCurrentMode(context.Context, string, string, string, RegistrationMode) (*User, error) {
	return nil, nil
}
func (TestStore) InviteCodeExists(context.Context, string) (bool, error)   { return false, nil }
func (TestStore) GetUserByUsername(context.Context, string) (*User, error) { return nil, nil }
func (TestStore) GetUserByID(context.Context, string) (*User, error)       { return nil, ErrUserNotFound }
func (TestStore) ListUsersPage(context.Context, *TimeIDCursor, int) (*UserPage, error) {
	return &UserPage{}, nil
}
func (TestStore) UpdateUser(context.Context, string, UserUpdates) (*User, error) {
	return nil, nil
}
func (TestStore) CountUsers(context.Context) (int64, error)         { return 0, nil }
func (TestStore) CountEnabledAdmins(context.Context) (int64, error) { return 0, nil }
func (TestStore) ReserveSuccessCall(context.Context, string, int) (SuccessQuotaReservation, error) {
	return SuccessQuotaReservation{}, nil
}
func (TestStore) ReleaseSuccessCall(context.Context, SuccessQuotaReservation) error { return nil }

func (TestStore) GetTierByID(context.Context, string) (*Tier, error) { return nil, ErrTierNotFound }
func (TestStore) GetTiersByIDs(context.Context, []string) (map[string]*Tier, error) {
	return map[string]*Tier{}, nil
}
func (TestStore) GetTierByName(context.Context, string) (*Tier, error) { return nil, nil }
func (TestStore) ListTiersPage(context.Context, *TierCursor, int) (*TierPage, error) {
	return &TierPage{}, nil
}
func (TestStore) CreateTier(context.Context, string, int, int, int) (*Tier, error) {
	return nil, nil
}
func (TestStore) UpdateTier(context.Context, string, TierUpdates) (*Tier, error) {
	return nil, nil
}
func (TestStore) DeleteTier(context.Context, string) error                { return nil }
func (TestStore) CountUsersByTier(context.Context, string) (int64, error) { return 0, nil }

func (TestStore) CreateKey(context.Context, string, string, int) (*APIKey, string, error) {
	return nil, "", nil
}
func (TestStore) ConfigureAPIKeyEncryption(string) error                { return nil }
func (TestStore) RevealKey(context.Context, string) (string, error)     { return "", nil }
func (TestStore) GetKeyByHash(context.Context, string) (*APIKey, error) { return nil, nil }
func (TestStore) ListKeysByUserPage(context.Context, string, *TimeIDCursor, int) (*APIKeyPage, error) {
	return &APIKeyPage{}, nil
}
func (TestStore) GetKeyByID(context.Context, string) (*APIKey, error) { return nil, nil }
func (TestStore) UpdateKey(context.Context, string, KeyUpdates) (*APIKey, error) {
	return nil, nil
}
func (TestStore) DeleteKey(context.Context, string) error { return nil }
func (TestStore) RecordUsage(context.Context, UsageRecord) error {
	return nil
}
func (TestStore) GetUsageStats(context.Context, string, time.Time) (*UsageStats, error) {
	return nil, nil
}
func (TestStore) GetUserUsageStats(context.Context, string, time.Time) (*UsageStats, error) {
	return nil, nil
}
func (TestStore) GetUserUsageStatsPage(context.Context, string, time.Time, *UsageRecordCursor, int) (*UsageStats, error) {
	return nil, nil
}
func (TestStore) GetGlobalStats(context.Context, time.Time) (*UsageStats, error) {
	return nil, nil
}
func (TestStore) ListUsageRecordsPage(context.Context, UsageRecordListScope, time.Time, *UsageRecordCursor, int) (*UsageRecordPage, error) {
	return &UsageRecordPage{}, nil
}
func (TestStore) GetUsageRecordDetail(context.Context, int64, UsageRecordScope) (*UsageRecord, error) {
	return nil, nil
}
func (TestStore) TouchKeyUsage(context.Context, string) error { return nil }

func (TestStore) GetServerSettings(context.Context) (*ServerSettings, error) { return nil, nil }
func (TestStore) UpsertServerSettings(context.Context, ServerSettings) (*ServerSettings, error) {
	return nil, nil
}

func (TestStore) ListInviteCodesPage(context.Context, *TimeIDCursor, int) (*InviteCodePage, error) {
	return &InviteCodePage{}, nil
}
func (TestStore) ListInviteCodeRedemptionsPage(context.Context, string, *TimeIDCursor, int) (*InviteCodeRedemptionPage, error) {
	return &InviteCodeRedemptionPage{}, nil
}
func (TestStore) CreateInviteCode(context.Context, string, int) (*InviteCode, string, error) {
	return nil, "", nil
}
func (TestStore) UpdateInviteCode(context.Context, string, InviteCodeUpdates) (*InviteCode, error) {
	return nil, nil
}
func (TestStore) DeleteInviteCode(context.Context, string) error { return nil }
