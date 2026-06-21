// Package admin 提供 /admin/v1 REST API：API Key 生命周期管理与用量查询。
package admin

import (
	"time"

	"github.com/grok-mcp/internal/store"
)

// CreateKeyRequest POST /admin/v1/keys 的请求体。
type CreateKeyRequest struct {
	Name      string `json:"name"`
	RateLimit int    `json:"rate_limit,omitempty"`
}

// CreateKeyResponse 创建成功时返回元数据与一次性明文 api_key。
type CreateKeyResponse struct {
	Key    KeyResponse `json:"key"`
	APIKey string      `json:"api_key"`
}

// UpdateKeyRequest PATCH 可部分更新 name、rate_limit、enabled。
type UpdateKeyRequest struct {
	Name      *string `json:"name,omitempty"`
	RateLimit *int    `json:"rate_limit,omitempty"`
	Enabled   *bool   `json:"enabled,omitempty"`
}

// KeyResponse 对外暴露的密钥视图（不含 hash / 明文）。
type KeyResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	RateLimit  int        `json:"rate_limit"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	TotalCalls int64      `json:"total_calls"`
}

// UsageStatsResponse 与 store.UsageStats 对应的 JSON 形状。
type UsageStatsResponse struct {
	TotalCalls int64            `json:"total_calls"`
	ByTool     map[string]int64 `json:"by_tool"`
	Records    []UsageRecordDTO `json:"records,omitempty"`
}

// UsageRecordDTO 单条调用明细的 API 表示。
type UsageRecordDTO struct {
	ID         int64     `json:"id"`
	KeyID      string    `json:"key_id"`
	ToolName   string    `json:"tool_name"`
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"duration_ms"`
}

func toKeyResponse(k *store.APIKey) KeyResponse {
	return KeyResponse{
		ID:         k.ID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		RateLimit:  k.RateLimit,
		Enabled:    k.Enabled,
		CreatedAt:  k.CreatedAt,
		UpdatedAt:  k.UpdatedAt,
		LastUsedAt: k.LastUsedAt,
		TotalCalls: k.TotalCalls,
	}
}

func toUsageStatsResponse(s *store.UsageStats) UsageStatsResponse {
	out := UsageStatsResponse{
		TotalCalls: s.TotalCalls,
		ByTool:     s.ByTool,
	}
	for _, r := range s.Records {
		out.Records = append(out.Records, UsageRecordDTO{
			ID: r.ID, KeyID: r.KeyID, ToolName: r.ToolName,
			Timestamp: r.Timestamp, DurationMs: r.DurationMs,
		})
	}
	return out
}