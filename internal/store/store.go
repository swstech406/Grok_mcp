// Package store 定义 API Key、用户与用量日志的持久化抽象及领域模型（HTTP 模式使用 SQLite 实现）。
package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/settings"
)

// ErrUserNotFound 表示按 ID 未找到用户。
var ErrUserNotFound = errors.New("user not found")

// ErrLastAdmin 表示试图移除系统中最后一个启用中的管理员用户。
var ErrLastAdmin = errors.New("cannot remove last enabled admin")

// ErrUsernameTaken 表示用户名已存在。
var ErrUsernameTaken = errors.New("username already taken")

// ErrTierNotFound 表示按 ID 未找到等级。
var ErrTierNotFound = errors.New("tier not found")

// ErrTierNameTaken 表示等级名称已存在。
var ErrTierNameTaken = errors.New("tier name already taken")

// ErrTierInUse 表示等级仍被用户引用，不能删除。
var ErrTierInUse = errors.New("tier in use")

// ErrTierNotAssignable 表示 tier_id 为空或不存在，不能分配给用户。
// 任意已存在的 tier 均可分配（不再限制 name 必须为 tier0~tier6）。
var ErrTierNotAssignable = errors.New("tier_id must reference an existing tier")

// ErrQuotaSuccess 表示用户成功请求额度已耗尽。
var ErrQuotaSuccess = errors.New("success request limit exceeded")

// ErrAPIKeyLimit indicates that a user already owns the configured maximum
// number of API keys. Disabled keys count toward this storage limit.
var ErrAPIKeyLimit = errors.New("API key limit reached")

// ErrInviteCodeNotFound 表示按 ID 未找到邀请码。
var ErrInviteCodeNotFound = errors.New("invite code not found")

// ErrInviteCodeInvalid 表示注册时提供的邀请码不存在或格式无效。
var ErrInviteCodeInvalid = errors.New("invalid invite code")

// ErrInviteCodeDisabled 表示邀请码已被管理员禁用。
var ErrInviteCodeDisabled = errors.New("invite code disabled")

// ErrInviteCodeExhausted 表示邀请码已达到注册上限。
var ErrInviteCodeExhausted = errors.New("invite code exhausted")

// ErrInviteCodeLimitTooLow 表示新的注册上限低于当前已使用次数。
var ErrInviteCodeLimitTooLow = errors.New("invite code registration limit is lower than current usage")

// ErrUsageRecordNotFound is returned when a usage record does not exist or is
// outside the caller's authorized scope.
var ErrUsageRecordNotFound = errors.New("usage record not found")

// DefaultTierName 是新建用户默认分配的等级名称。
const DefaultTierName = "tier0"

// RegistrationMode 控制公开注册入口如何放行新用户。
type RegistrationMode = settings.RegistrationMode

const (
	// RegistrationModeFree 允许用户无需邀请码自助注册。
	RegistrationModeFree = settings.RegistrationModeFree
	// RegistrationModeInvite 要求用户提供有效且未耗尽的邀请码注册。
	RegistrationModeInvite = settings.RegistrationModeInvite
	// RegistrationModeDisabled 禁止公开自助注册。
	RegistrationModeDisabled = settings.RegistrationModeDisabled
)

// NormalizeRegistrationMode canonicalizes the persisted/server setting value.
func NormalizeRegistrationMode(mode RegistrationMode) (RegistrationMode, error) {
	return settings.NormalizeRegistrationMode(mode)
}

// UserRole 面板用户角色。
type UserRole string

const (
	RoleAdmin UserRole = "admin"
	RoleUser  UserRole = "user"
)

// User 表示面板注册用户的持久化实体。
// 限额（RPM / SuccessLimit）不在本结构中：它们属于运行时视图（auth.AuthenticatedUser），
// 由所属 tier 在鉴权链路中合并，避免持久化实体混入派生字段。
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         UserRole
	Enabled      bool
	TierID       string
	SuccessCalls int64
	// SuccessPeriod 是 success_calls 所属的 UTC 月份（YYYY-MM）。success_calls 表示该月成功调用数，
	// 进入新月份后会在读写用户或预留 quota 时重置为 0。
	SuccessPeriod string
	// TokenVersion 写入 JWT 的 "tv" 声明；中间件比对 DB 当前值，不一致即拒签。
	// 角色/启用状态变更或显式吊销时自增，令所有未刷新的 token 立即失效。
	TokenVersion int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SuccessQuotaReservation identifies the exact monthly quota bucket incremented
// for one admitted MCP tool call. Rollback must use this value instead of
// deriving mutable accounting dimensions again.
type SuccessQuotaReservation struct {
	UserID string
	Period string
}

// IsValid reports whether the token identifies one complete UTC monthly quota
// bucket. It does not check whether that bucket is currently active.
func (reservation SuccessQuotaReservation) IsValid() bool {
	if strings.TrimSpace(reservation.UserID) == "" || strings.TrimSpace(reservation.Period) == "" {
		return false
	}
	parsedPeriod, err := time.Parse(successQuotaPeriodLayout, reservation.Period)
	return err == nil && parsedPeriod.Format(successQuotaPeriodLayout) == reservation.Period
}

// Tier 表示用户等级预设（预置 tier0~tier6，也可自定义），是用户限额（rpm/success_limit）的唯一来源。
type Tier struct {
	ID           string
	Name         string
	Level        int
	RPM          int
	SuccessLimit int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// APIKey 表示一条已发放的客户端密钥。KeyHash 用于鉴权，可恢复内容以 AEAD 密文保存。
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

	keyCiphertext        string
	keyNonce             string
	keyEncryptionVersion int
}

// InviteCode represents invite metadata. CodeHash is authoritative for
// redemption; raw invite material is returned only by CreateInviteCode.
type InviteCode struct {
	ID                string
	CodeHash          string
	CodePrefix        string
	RegistrationLimit int
	RegistrationCount int
	Enabled           bool
	CreatedByUserID   string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// InviteCodeRedemption records the account created by one successful invite
// redemption. The identity fields are immutable snapshots so audit history is
// retained even if the invite code or user is later deleted.
type InviteCodeRedemption struct {
	ID               string
	InviteCodeID     string
	InviteCodePrefix string
	UserID           string
	Username         string
	RedeemedAt       time.Time
}

// UsageRecord 为单次 MCP 工具调用的明细日志（工具名、耗时、是否成功等）。
type UsageRecord struct {
	ID         int64
	KeyID      string
	ToolName   string
	Timestamp  time.Time
	DurationMs int64
	Success    bool
	// DebugJSON stores compact debug metadata. Complete bodies are persisted
	// separately so queued records never retain potentially huge strings.
	DebugJSON string
	// Debug body summaries are populated in one batched metadata query for usage
	// lists. The complete BLOB contents are loaded only by GetUsageRecordDetail.
	HasDebugRequestBody        bool
	HasDebugResponseBody       bool
	DebugRequestBytes          int64
	DebugResponseBytes         int64
	DebugRequestObservedBytes  int64
	DebugResponseObservedBytes int64
	DebugRequestTruncated      bool
	DebugResponseTruncated     bool
	// DebugRequestBodyPath and DebugResponseBodyPath are short-lived spool file
	// references consumed transactionally by RecordUsage. They are never
	// returned from usage-stat queries.
	DebugRequestBodyPath  string
	DebugResponseBodyPath string
	// DebugRequestBody and DebugResponseBody are populated only by the explicit
	// single-record detail query. Usage-stat list queries always leave them empty.
	DebugRequestBody  string
	DebugResponseBody string
	// Cleanup releases resources owned by the queued record, such as temporary
	// debug capture files. AsyncUsageWriter invokes it exactly once after the
	// record is written or discarded.
	Cleanup func()
}

// UsageBucket 表示流量图中的一个时间桶；Start 包含，End 除最后一个桶外不包含。
type UsageBucket struct {
	Start time.Time
	End   time.Time
	Calls int64
}

// UsageStats 聚合某段时间内的调用统计；Records 保存调用方请求的数据库记录页，
// CurrentRPM 基于原始日志，历史总量与 TrafficBuckets 会合并小时级和日级聚合。
type UsageStats struct {
	TotalCalls     int64
	SuccessCalls   int64
	CurrentRPM     int64
	ByTool         map[string]int64
	TrafficBuckets []UsageBucket
	Records        []UsageRecord
	RecordsPage    UsageRecordPageInfo
}

// TimeIDCursor is a stable keyset boundary for collections ordered by time and ID.
type TimeIDCursor struct {
	Timestamp time.Time
	ID        string
}

// TierCursor is a stable keyset boundary for tiers ordered by level, name, and ID.
type TierCursor struct {
	Level int
	Name  string
	ID    string
}

// UsageRecordCursor is a stable keyset boundary for usage records ordered newest first.
type UsageRecordCursor struct {
	Timestamp time.Time
	ID        int64
}

type UsageRecordPageInfo struct {
	HasMore    bool
	NextCursor *UsageRecordCursor
}

type APIKeyPage struct {
	Keys        []*APIKey
	TotalCount  int64
	ActiveCount int64
	HasMore     bool
	NextCursor  *TimeIDCursor
}

type UserPage struct {
	Users      []*User
	TotalCount int64
	HasMore    bool
	NextCursor *TimeIDCursor
}

type TierPage struct {
	Tiers      []*Tier
	TotalCount int64
	HasMore    bool
	NextCursor *TierCursor
}

type InviteCodePage struct {
	InviteCodes []*InviteCode
	TotalCount  int64
	HasMore     bool
	NextCursor  *TimeIDCursor
}

type InviteCodeRedemptionPage struct {
	Redemptions []*InviteCodeRedemption
	HasMore     bool
	NextCursor  *TimeIDCursor
}

type UsageRecordPage struct {
	Records    []UsageRecord
	HasMore    bool
	NextCursor *UsageRecordCursor
}

// UsageRecordListScope constrains a usage-history page to one key, one user,
// or all users. IncludeAllUsers takes precedence over UserID.
type UsageRecordListScope struct {
	KeyID           string
	UserID          string
	IncludeAllUsers bool
}

// UsageRecordScope constrains access to a single usage record. Administrators
// may set IncludeAllUsers; normal callers must provide their authenticated user
// ID so ownership is checked in SQL before debug bodies are loaded.
type UsageRecordScope struct {
	UserID          string
	IncludeAllUsers bool
}

// ServerSettings stores runtime-tunable MCP server configuration. Secrets are
// persisted because the server must reconnect to the upstream gateway after a
// restart without exposing them back through the panel API.
type ServerSettings struct {
	settings.Runtime
	ID        string
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// KeyUpdates 用于 PATCH 式更新密钥；指针字段为 nil 表示不修改该列。
type KeyUpdates struct {
	Name    *string
	Enabled *bool
}

// UserUpdates 用于管理员 PATCH 用户；限额由 tier 决定，此处仅允许调整 enabled/role/tier_id。
// RevokeTokens 为 true 时自增 token_version，强制该用户所有未刷新的 JWT 立即失效（强制下线）。
// PasswordHash 用于重置密码（如 bootstrap admin 接管已存在的同名用户）。
type UserUpdates struct {
	Enabled      *bool
	Role         *UserRole
	TierID       *string
	RevokeTokens *bool
	PasswordHash *string
}

// TierUpdates 用于管理员 PATCH 等级；指针字段为 nil 表示不修改。
type TierUpdates struct {
	Name         *string
	Level        *int
	RPM          *int
	SuccessLimit *int
}

// InviteCodeUpdates 用于管理员 PATCH 邀请码；nil 表示不修改对应字段。
type InviteCodeUpdates struct {
	RegistrationLimit *int
	Enabled           *bool
}

// Store 是用户、密钥 CRUD 与用量读写的接口，便于测试注入 mock。
type Store interface {
	Close() error

	// CreateUser 建用户；限额不再随用户保存，统一由默认 tier0 决定。
	CreateUser(ctx context.Context, username, passwordHash string, role UserRole) (*User, error)
	// RegisterUser 自助注册：在同一事务内判断是否为首个用户并赋 admin，避免并发双管理员。
	RegisterUser(ctx context.Context, username, passwordHash string) (*User, error)
	// RegisterUserWithInviteCode 在同一事务中校验/消耗邀请码并创建普通用户。
	RegisterUserWithInviteCode(ctx context.Context, username, passwordHash, rawInviteCode string) (*User, error)
	// RegisterUserWithCurrentMode 在同一事务内读取当前注册模式并按该模式创建用户。
	RegisterUserWithCurrentMode(ctx context.Context, username, passwordHash, rawInviteCode string, fallbackMode RegistrationMode) (*User, error)
	// InviteCodeExists performs a cheap non-authoritative lookup before password hashing.
	InviteCodeExists(ctx context.Context, rawInviteCode string) (bool, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	ListUsersPage(ctx context.Context, cursor *TimeIDCursor, limit int) (*UserPage, error)
	UpdateUser(ctx context.Context, id string, updates UserUpdates) (*User, error)
	DeleteUser(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int64, error)
	CountEnabledAdmins(ctx context.Context) (int64, error)
	ReserveSuccessCall(ctx context.Context, userID string, successLimit int) (SuccessQuotaReservation, error)
	ReleaseSuccessCall(ctx context.Context, reservation SuccessQuotaReservation) error

	GetTierByID(ctx context.Context, id string) (*Tier, error)
	GetTiersByIDs(ctx context.Context, ids []string) (map[string]*Tier, error)
	GetTierByName(ctx context.Context, name string) (*Tier, error)
	ListTiersPage(ctx context.Context, cursor *TierCursor, limit int) (*TierPage, error)
	CreateTier(ctx context.Context, name string, level, rpm, successLimit int) (*Tier, error)
	UpdateTier(ctx context.Context, id string, updates TierUpdates) (*Tier, error)
	DeleteTier(ctx context.Context, id string) error
	CountUsersByTier(ctx context.Context, tierID string) (int64, error)

	CreateKey(ctx context.Context, userID, name string, maximumKeys int) (*APIKey, string, error)
	ConfigureAPIKeyEncryption(applicationSecret string) error
	RevealKey(ctx context.Context, id string) (string, error)
	GetKeyByHash(ctx context.Context, hash string) (*APIKey, error)
	ListKeysByUserPage(ctx context.Context, userID string, cursor *TimeIDCursor, limit int) (*APIKeyPage, error)
	GetKeyByID(ctx context.Context, id string) (*APIKey, error)
	UpdateKey(ctx context.Context, id string, updates KeyUpdates) (*APIKey, error)
	DeleteKey(ctx context.Context, id string) error
	RecordUsage(ctx context.Context, record UsageRecord) error
	GetUsageStats(ctx context.Context, keyID string, since time.Time) (*UsageStats, error)
	GetUserUsageStats(ctx context.Context, userID string, since time.Time) (*UsageStats, error)
	GetUserUsageStatsPage(ctx context.Context, userID string, since time.Time, cursor *UsageRecordCursor, limit int) (*UsageStats, error)
	GetGlobalStats(ctx context.Context, since time.Time) (*UsageStats, error)
	ListUsageRecordsPage(ctx context.Context, scope UsageRecordListScope, since time.Time, cursor *UsageRecordCursor, limit int) (*UsageRecordPage, error)
	GetUsageRecordDetail(ctx context.Context, usageID int64, scope UsageRecordScope) (*UsageRecord, error)
	ListInviteCodesPage(ctx context.Context, cursor *TimeIDCursor, limit int) (*InviteCodePage, error)
	ListInviteCodeRedemptionsPage(ctx context.Context, inviteCodeID string, cursor *TimeIDCursor, limit int) (*InviteCodeRedemptionPage, error)
	CreateInviteCode(ctx context.Context, createdByUserID string, registrationLimit int) (*InviteCode, string, error)
	UpdateInviteCode(ctx context.Context, id string, updates InviteCodeUpdates) (*InviteCode, error)
	DeleteInviteCode(ctx context.Context, id string) error

	GetServerSettings(ctx context.Context) (*ServerSettings, error)
	UpsertServerSettings(ctx context.Context, settings ServerSettings) (*ServerSettings, error)
}
