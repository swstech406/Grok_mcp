// Package store 定义 API Key 与用量日志的持久化抽象及领域模型（HTTP 模式使用 SQLite 实现）。
package store

import (
	"context"
	"time"
)

// APIKey 表示一条已发放的客户端密钥。数据库只存 SHA-256 哈希；明文仅在 CreateKey 时返回一次。
type APIKey struct {
	ID         string
	Name       string
	KeyHash    string
	KeyPrefix  string
	RateLimit  int
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastUsedAt *time.Time
	TotalCalls int64
}

// UsageRecord 为单次 MCP 工具调用的明细日志（工具名、耗时等）。
type UsageRecord struct {
	ID         int64
	KeyID      string
	ToolName   string
	Timestamp  time.Time
	DurationMs int64
}

// UsageStats 聚合某段时间内的调用统计；Records 最多返回最近 500 条明细。
type UsageStats struct {
	TotalCalls   int64
	ByTool       map[string]int64
	Records      []UsageRecord
}

// KeyUpdates 用于 PATCH 式更新密钥；指针字段为 nil 表示不修改该列。
type KeyUpdates struct {
	Name      *string
	RateLimit *int
	Enabled   *bool
}

// Store 是密钥 CRUD 与用量读写的接口，便于测试注入 mock。
type Store interface {
	Close() error
	CreateKey(ctx context.Context, name string, rateLimit int) (*APIKey, string, error)
	GetKeyByHash(ctx context.Context, hash string) (*APIKey, error)
	ListKeys(ctx context.Context) ([]*APIKey, error)
	GetKeyByID(ctx context.Context, id string) (*APIKey, error)
	UpdateKey(ctx context.Context, id string, updates KeyUpdates) (*APIKey, error)
	DeleteKey(ctx context.Context, id string) error
	RecordUsage(ctx context.Context, record UsageRecord) error
	GetUsageStats(ctx context.Context, keyID string, since time.Time) (*UsageStats, error)
	GetGlobalStats(ctx context.Context, since time.Time) (*UsageStats, error)
	TouchKeyUsage(ctx context.Context, keyID string) error
}