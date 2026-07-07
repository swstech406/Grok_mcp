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
func (TestStore) GetUserByUsername(context.Context, string) (*User, error) { return nil, nil }
func (TestStore) GetUserByID(context.Context, string) (*User, error)       { return nil, ErrUserNotFound }
func (TestStore) ListUsers(context.Context) ([]*User, error)               { return nil, nil }
func (TestStore) UpdateUser(context.Context, string, UserUpdates) (*User, error) {
	return nil, nil
}
func (TestStore) CountUsers(context.Context) (int64, error)         { return 0, nil }
func (TestStore) CountEnabledAdmins(context.Context) (int64, error) { return 0, nil }
func (TestStore) ReserveSuccessCall(context.Context, string, int) error {
	return nil
}
func (TestStore) ReleaseSuccessCall(context.Context, string) error { return nil }
func (TestStore) TryIncrementUserSuccessCalls(context.Context, string, int) error {
	return nil
}

func (TestStore) GetTierByID(context.Context, string) (*Tier, error)   { return nil, ErrTierNotFound }
func (TestStore) GetTierByName(context.Context, string) (*Tier, error) { return nil, nil }
func (TestStore) ListTiers(context.Context) ([]*Tier, error)           { return nil, nil }
func (TestStore) CreateTier(context.Context, string, int, int, int) (*Tier, error) {
	return nil, nil
}
func (TestStore) UpdateTier(context.Context, string, TierUpdates) (*Tier, error) {
	return nil, nil
}
func (TestStore) DeleteTier(context.Context, string) error                { return nil }
func (TestStore) CountUsersByTier(context.Context, string) (int64, error) { return 0, nil }

func (TestStore) CreateKey(context.Context, string, string) (*APIKey, string, error) {
	return nil, "", nil
}
func (TestStore) GetKeyByHash(context.Context, string) (*APIKey, error) { return nil, nil }
func (TestStore) ListKeys(context.Context) ([]*APIKey, error)           { return nil, nil }
func (TestStore) ListKeysByUser(context.Context, string) ([]*APIKey, error) {
	return nil, nil
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
func (TestStore) GetGlobalStats(context.Context, time.Time) (*UsageStats, error) {
	return nil, nil
}
func (TestStore) TouchKeyUsage(context.Context, string) error { return nil }

func (TestStore) GetServerSettings(context.Context) (*ServerSettings, error) { return nil, nil }
func (TestStore) UpsertServerSettings(context.Context, ServerSettings) (*ServerSettings, error) {
	return nil, nil
}
