// Package store 定义 API Key、用户与用量日志的持久化抽象及领域模型（HTTP 模式使用 SQLite 实现）。
package store

import (
	"context"
	"errors"
	"time"
)

// ErrUserNotFound 表示按 ID 未找到用户。
var ErrUserNotFound = errors.New("user not found")

// ErrLastAdmin 表示试图删除系统中最后一个管理员用户。
var ErrLastAdmin = errors.New("cannot delete last admin")

// ErrUsernameTaken 表示用户名已存在。
var ErrUsernameTaken = errors.New("username already taken")

// ErrTierNotFound 表示按 ID 未找到等级。
var ErrTierNotFound = errors.New("tier not found")

// ErrTierNameTaken 表示等级名称已存在。
var ErrTierNameTaken = errors.New("tier name already taken")

// ErrTierInUse 表示等级仍被用户引用，不能删除。
var ErrTierInUse = errors.New("tier in use")

// ErrQuotaSuccess 表示用户成功请求额度已耗尽。
var ErrQuotaSuccess = errors.New("success request limit exceeded")

// UserRole 面板用户角色。
type UserRole string

const (
	RoleAdmin UserRole = "admin"
	RoleUser  UserRole = "user"
)

// User 表示面板注册用户及其汇总额度与计数。
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         UserRole
	Enabled      bool
	TierID       string
	// RPM / SuccessLimit 不再持久化到 users 表，也不再作为限额来源。
	// 它们是请求链路上由 auth.LoadUserWithTierLimits 就地写入的“生效限额”，
	// 取值完全来自用户所属 tier（tier 缺失时回退 tier0），仅供限流/额度中间件读取。
	RPM          int
	SuccessLimit int
	SuccessCalls int64
	// TokenVersion 写入 JWT 的 "tv" 声明；中间件比对 DB 当前值，不一致即拒签。
	// 角色/启用状态变更或显式吊销时自增，令所有未刷新的 token 立即失效。
	TokenVersion int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Tier 表示用户等级预设（tier0~tier6），是用户限额（rpm/success_limit）的唯一来源。
type Tier struct {
	ID           string
	Name         string
	Level        int
	RPM          int
	SuccessLimit int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// APIKey 表示一条已发放的客户端密钥。数据库只存 keyhash.HashAPIKey 结果；明文仅在 CreateKey 时返回一次。
type APIKey struct {
	ID         string
	UserID     string
	Name       string
	KeyHash    string
	KeyPrefix  string
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastUsedAt *time.Time
	TotalCalls int64
}

// UsageRecord 为单次 MCP 工具调用的明细日志（工具名、耗时、是否成功等）。
type UsageRecord struct {
	ID         int64
	KeyID      string
	ToolName   string
	Timestamp  time.Time
	DurationMs int64
	Success    bool
	// TouchKey 为 true 时异步执行 TouchKeyUsage，不写 usage_log。
	TouchKey bool
}

// UsageStats 聚合某段时间内的调用统计；Records 最多返回最近 500 条明细。
type UsageStats struct {
	TotalCalls   int64
	SuccessCalls int64
	ByTool       map[string]int64
	Records      []UsageRecord
}

// KeyUpdates 用于 PATCH 式更新密钥；指针字段为 nil 表示不修改该列。
type KeyUpdates struct {
	Name    *string
	Enabled *bool
}

// UserUpdates 用于管理员 PATCH 用户；限额由 tier 决定，此处仅允许调整 enabled/role/tier_id。
// RevokeTokens 为 true 时自增 token_version，强制该用户所有未刷新的 JWT 立即失效（强制下线）。
type UserUpdates struct {
	Enabled      *bool
	Role         *UserRole
	TierID       *string
	RevokeTokens *bool
}

// TierUpdates 用于管理员 PATCH 等级；指针字段为 nil 表示不修改。
type TierUpdates struct {
	Name         *string
	Level        *int
	RPM          *int
	SuccessLimit *int
}

// Store 是用户、密钥 CRUD 与用量读写的接口，便于测试注入 mock。
type Store interface {
	Close() error

	// CreateUser 建用户；限额不再随用户保存，统一由默认 tier0 决定。
	CreateUser(ctx context.Context, username, passwordHash string, role UserRole) (*User, error)
	// RegisterUser 自助注册：在同一事务内判断是否为首个用户并赋 admin，避免并发双管理员。
	RegisterUser(ctx context.Context, username, passwordHash string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	ListUsers(ctx context.Context) ([]*User, error)
	UpdateUser(ctx context.Context, id string, updates UserUpdates) (*User, error)
	DeleteUser(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int64, error)
	ReserveSuccessCall(ctx context.Context, userID string, successLimit int) error
	ReleaseSuccessCall(ctx context.Context, userID string) error
	TryIncrementUserSuccessCalls(ctx context.Context, userID string, successLimit int) error

	GetTierByID(ctx context.Context, id string) (*Tier, error)
	GetTierByName(ctx context.Context, name string) (*Tier, error)
	ListTiers(ctx context.Context) ([]*Tier, error)
	CreateTier(ctx context.Context, name string, level, rpm, successLimit int) (*Tier, error)
	UpdateTier(ctx context.Context, id string, updates TierUpdates) (*Tier, error)
	DeleteTier(ctx context.Context, id string) error
	CountUsersByTier(ctx context.Context, tierID string) (int64, error)

	CreateKey(ctx context.Context, userID, name string) (*APIKey, string, error)
	GetKeyByHash(ctx context.Context, hash string) (*APIKey, error)
	ListKeys(ctx context.Context) ([]*APIKey, error)
	ListKeysByUser(ctx context.Context, userID string) ([]*APIKey, error)
	GetKeyByID(ctx context.Context, id string) (*APIKey, error)
	UpdateKey(ctx context.Context, id string, updates KeyUpdates) (*APIKey, error)
	DeleteKey(ctx context.Context, id string) error
	RecordUsage(ctx context.Context, record UsageRecord) error
	GetUsageStats(ctx context.Context, keyID string, since time.Time) (*UsageStats, error)
	GetUserUsageStats(ctx context.Context, userID string, since time.Time) (*UsageStats, error)
	GetGlobalStats(ctx context.Context, since time.Time) (*UsageStats, error)
	TouchKeyUsage(ctx context.Context, keyID string) error
}
