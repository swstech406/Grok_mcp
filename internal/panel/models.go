// Package panel 提供 /panel/v1 管理面板 REST API。
package panel

import (
	"time"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/grok"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/version"
)

type RegisterRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	InviteCode string `json:"invite_code,omitempty"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token     string       `json:"token"`
	ExpiresAt time.Time    `json:"expires_at"`
	User      UserResponse `json:"user"`
}

type UserResponse struct {
	ID        string         `json:"id"`
	Username  string         `json:"username"`
	Role      store.UserRole `json:"role"`
	Enabled   bool           `json:"enabled"`
	TierID    string         `json:"tier_id,omitempty"`
	TierName  string         `json:"tier_name,omitempty"`
	TierLevel *int           `json:"tier_level,omitempty"`
	// LimitsUnavailable 为 true 表示未能解析所属 tier，rpm/success_limit 不可信（勿当作 0=不限）。
	LimitsUnavailable bool      `json:"limits_unavailable,omitempty"`
	RPM               int       `json:"rpm"`
	SuccessLimit      int       `json:"success_limit"`
	SuccessCalls      int64     `json:"success_calls"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type CreateKeyRequest struct {
	Name string `json:"name"`
}

type CreateKeyResponse struct {
	Key    KeyResponse `json:"key"`
	APIKey string      `json:"api_key"`
}

type RevealKeyResponse struct {
	APIKey string `json:"api_key"`
}

type UpdateKeyRequest struct {
	Name    *string `json:"name,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

type KeyResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	TotalCalls int64      `json:"total_calls"`
}

type KeysResponse struct {
	Keys        []KeyResponse `json:"keys"`
	NextCursor  string        `json:"next_cursor,omitempty"`
	HasMore     bool          `json:"has_more"`
	TotalCount  int64         `json:"total_count"`
	ActiveCount int64         `json:"active_count"`
}

type UsersResponse struct {
	Users      []UserResponse `json:"users"`
	NextCursor string         `json:"next_cursor,omitempty"`
	HasMore    bool           `json:"has_more"`
	TotalCount int64          `json:"total_count"`
}

type TiersResponse struct {
	Tiers             []TierResponse `json:"tiers"`
	NextCursor        string         `json:"next_cursor,omitempty"`
	HasMore           bool           `json:"has_more"`
	TotalCount        int64          `json:"total_count"`
	AssignedUserCount int64          `json:"assigned_user_count"`
}

// UpdateUserRequest 仅允许调整 enabled/role/tier_id；限额（rpm/success_limit）
// 由所属 tier 决定，不再支持按用户单独设置。RevokeTokens=true 强制吊销该用户所有存量 JWT。
type UpdateUserRequest struct {
	Enabled      *bool           `json:"enabled,omitempty"`
	Role         *store.UserRole `json:"role,omitempty"`
	TierID       *string         `json:"tier_id,omitempty"`
	RevokeTokens *bool           `json:"revoke_tokens,omitempty"`
}

// TierResponse 为等级预设的对外表示。
type TierResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Level        int       `json:"level"`
	RPM          int       `json:"rpm"`
	SuccessLimit int       `json:"success_limit"`
	UserCount    int64     `json:"user_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CreateTierRequest struct {
	Name         string `json:"name"`
	Level        int    `json:"level"`
	RPM          int    `json:"rpm"`
	SuccessLimit int    `json:"success_limit"`
}

type UpdateTierRequest struct {
	Name         *string `json:"name,omitempty"`
	Level        *int    `json:"level,omitempty"`
	RPM          *int    `json:"rpm,omitempty"`
	SuccessLimit *int    `json:"success_limit,omitempty"`
}

type ServerSettingsResponse struct {
	Version                    string                  `json:"version"`
	CPABaseURL                 string                  `json:"cpa_base_url"`
	CPAAPIKeySet               bool                    `json:"cpa_api_key_set"`
	CPAAPIKeyPreview           string                  `json:"cpa_api_key_preview,omitempty"`
	UpstreamProtocol           config.UpstreamProtocol `json:"upstream_protocol"`
	Model                      string                  `json:"model"`
	TimeoutSeconds             int                     `json:"timeout_seconds"`
	MCPGlobalSearchConcurrency int                     `json:"mcp_global_search_concurrency"`
	MCPUserSearchConcurrency   int                     `json:"mcp_user_search_concurrency"`
	ProxyURL                   string                  `json:"proxy_url"`
	ProxyEnabled               bool                    `json:"proxy_enabled"`
	RegistrationMode           store.RegistrationMode  `json:"registration_mode"`
	Debug                      bool                    `json:"debug"`
	UpdatedAt                  *time.Time              `json:"updated_at,omitempty"`
}

type RegistrationSettingsResponse struct {
	RegistrationMode store.RegistrationMode `json:"registration_mode"`
}

type ModelResponse struct {
	ID string `json:"id"`
}

type ModelsResponse struct {
	Models []ModelResponse `json:"models"`
}

type UpdateServerSettingsRequest struct {
	CPABaseURL                 *string                  `json:"cpa_base_url,omitempty"`
	CPAAPIKey                  *string                  `json:"cpa_api_key,omitempty"`
	UpstreamProtocol           *config.UpstreamProtocol `json:"upstream_protocol,omitempty"`
	Model                      *string                  `json:"model,omitempty"`
	TimeoutSeconds             *int                     `json:"timeout_seconds,omitempty"`
	MCPGlobalSearchConcurrency *int                     `json:"mcp_global_search_concurrency,omitempty"`
	MCPUserSearchConcurrency   *int                     `json:"mcp_user_search_concurrency,omitempty"`
	ProxyURL                   *string                  `json:"proxy_url,omitempty"`
	ProxyEnabled               *bool                    `json:"proxy_enabled,omitempty"`
	RegistrationMode           *store.RegistrationMode  `json:"registration_mode,omitempty"`
	Debug                      *bool                    `json:"debug,omitempty"`
}

type InviteCodeResponse struct {
	ID                string    `json:"id"`
	Code              string    `json:"code,omitempty"`
	CodePrefix        string    `json:"code_prefix"`
	RegistrationLimit int       `json:"registration_limit"`
	RegistrationCount int       `json:"registration_count"`
	Enabled           bool      `json:"enabled"`
	CreatedByUserID   string    `json:"created_by_user_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type InviteCodesResponse struct {
	InviteCodes []InviteCodeResponse `json:"invite_codes"`
	NextCursor  string               `json:"next_cursor,omitempty"`
	HasMore     bool                 `json:"has_more"`
	TotalCount  int64                `json:"total_count"`
}

type CreateInviteCodeRequest struct {
	RegistrationLimit int `json:"registration_limit"`
}

type CreateInviteCodeResponse struct {
	InviteCode InviteCodeResponse `json:"invite_code"`
	Code       string             `json:"code"`
}

type UpdateInviteCodeRequest struct {
	RegistrationLimit *int  `json:"registration_limit,omitempty"`
	Enabled           *bool `json:"enabled,omitempty"`
}

type UsageStatsResponse struct {
	TotalCalls     int64            `json:"total_calls"`
	SuccessCalls   int64            `json:"success_calls"`
	CurrentRPM     int64            `json:"current_rpm"`
	ByTool         map[string]int64 `json:"by_tool"`
	TrafficBuckets []UsageBucketDTO `json:"traffic_buckets"`
	Records        []UsageRecordDTO `json:"records,omitempty"`
	NextCursor     string           `json:"next_cursor,omitempty"`
	HasMore        bool             `json:"has_more"`
}

type UsageRecordsResponse struct {
	Records    []UsageRecordDTO `json:"records"`
	NextCursor string           `json:"next_cursor,omitempty"`
	HasMore    bool             `json:"has_more"`
}

type UsageBucketDTO struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Calls int64     `json:"calls"`
}

type UsageRecordDTO struct {
	ID                     int64     `json:"id"`
	KeyID                  string    `json:"key_id"`
	ToolName               string    `json:"tool_name"`
	Timestamp              time.Time `json:"timestamp"`
	DurationMs             int64     `json:"duration_ms"`
	Success                bool      `json:"success"`
	DebugJSON              string    `json:"debug_json,omitempty"`
	HasDebugRequestBody    bool      `json:"has_debug_request_body,omitempty"`
	HasDebugResponseBody   bool      `json:"has_debug_response_body,omitempty"`
	DebugRequestBodyBytes  int64     `json:"debug_request_body_bytes,omitempty"`
	DebugResponseBodyBytes int64     `json:"debug_response_body_bytes,omitempty"`
}

type UsageRecordDetailDTO struct {
	ID                     int64     `json:"id"`
	KeyID                  string    `json:"key_id"`
	ToolName               string    `json:"tool_name"`
	Timestamp              time.Time `json:"timestamp"`
	DurationMs             int64     `json:"duration_ms"`
	Success                bool      `json:"success"`
	DebugJSON              string    `json:"debug_json,omitempty"`
	HasDebugRequestBody    bool      `json:"has_debug_request_body,omitempty"`
	HasDebugResponseBody   bool      `json:"has_debug_response_body,omitempty"`
	DebugRequestBodyBytes  int64     `json:"debug_request_body_bytes,omitempty"`
	DebugResponseBodyBytes int64     `json:"debug_response_body_bytes,omitempty"`
	DebugRequestBody       string    `json:"debug_request_body,omitempty"`
	DebugResponseBody      string    `json:"debug_response_body,omitempty"`
}

func toUserResponse(u *store.User) UserResponse {
	return UserResponse{
		ID: u.ID, Username: u.Username, Role: u.Role, Enabled: u.Enabled,
		TierID: u.TierID,
		// 未附带 tier 时不得把零值限额当成「不限」；由 toUserResponseWithTier 决定。
		LimitsUnavailable: true,
		RPM:               0,
		SuccessLimit:      0,
		SuccessCalls:      u.SuccessCalls,
		CreatedAt:         u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

// toUserResponseWithTier 填充用户关联的 tier 名称、等级与限额。限额以 tier 为唯一来源。
// tier 为 nil 时设置 LimitsUnavailable=true 且 rpm/success_limit 保持 0，避免面板把静默 0 显示成不限。
func toUserResponseWithTier(u *store.User, tier *store.Tier) UserResponse {
	resp := toUserResponse(u)
	if tier == nil {
		resp.LimitsUnavailable = true
		resp.RPM = 0
		resp.SuccessLimit = 0
		return resp
	}
	resp.LimitsUnavailable = false
	resp.TierName = tier.Name
	lvl := tier.Level
	resp.TierLevel = &lvl
	resp.RPM = tier.RPM
	resp.SuccessLimit = tier.SuccessLimit
	return resp
}

func toTierResponse(t *store.Tier, userCount int64) TierResponse {
	return TierResponse{
		ID: t.ID, Name: t.Name, Level: t.Level,
		RPM: t.RPM, SuccessLimit: t.SuccessLimit,
		UserCount: userCount, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func toServerSettingsResponse(settings config.ServerSettings, updatedAt *time.Time) ServerSettingsResponse {
	apiKeyPreview := ""
	if settings.CPAAPIKey != "" {
		apiKeyPreview = maskSecret(settings.CPAAPIKey)
	}
	return ServerSettingsResponse{
		Version:                    version.Version,
		CPABaseURL:                 settings.CPABaseURL,
		CPAAPIKeySet:               settings.CPAAPIKey != "",
		CPAAPIKeyPreview:           apiKeyPreview,
		UpstreamProtocol:           settings.UpstreamProtocol,
		Model:                      settings.Model,
		TimeoutSeconds:             settings.TimeoutSeconds,
		MCPGlobalSearchConcurrency: settings.MCPGlobalSearchConcurrency,
		MCPUserSearchConcurrency:   settings.MCPUserSearchConcurrency,
		ProxyURL:                   settings.ProxyURL,
		ProxyEnabled:               settings.ProxyEnabled,
		RegistrationMode:           settings.RegistrationMode,
		Debug:                      settings.Debug,
		UpdatedAt:                  updatedAt,
	}
}

func toInviteCodeResponse(inviteCode *store.InviteCode) InviteCodeResponse {
	return InviteCodeResponse{
		ID:                inviteCode.ID,
		Code:              inviteCode.Code,
		CodePrefix:        inviteCode.CodePrefix,
		RegistrationLimit: inviteCode.RegistrationLimit,
		RegistrationCount: inviteCode.RegistrationCount,
		Enabled:           inviteCode.Enabled,
		CreatedByUserID:   inviteCode.CreatedByUserID,
		CreatedAt:         inviteCode.CreatedAt,
		UpdatedAt:         inviteCode.UpdatedAt,
	}
}

func toModelsResponse(models []grok.Model) ModelsResponse {
	filteredModels := grok.FilterGrokModels(models)
	responseModels := make([]ModelResponse, 0, len(filteredModels))
	for _, model := range filteredModels {
		responseModels = append(responseModels, ModelResponse{ID: model.ID})
	}
	return ModelsResponse{Models: responseModels}
}

func maskSecret(secret string) string {
	if len(secret) <= 8 {
		return "configured"
	}
	return secret[:4] + "..." + secret[len(secret)-4:]
}

func toKeyResponse(k *store.APIKey) KeyResponse {
	return KeyResponse{
		ID: k.ID, Name: k.Name, KeyPrefix: k.KeyPrefix, Enabled: k.Enabled,
		CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt, LastUsedAt: k.LastUsedAt,
		TotalCalls: k.TotalCalls,
	}
}

func toUsageStatsResponse(s *store.UsageStats) UsageStatsResponse {
	out := UsageStatsResponse{
		TotalCalls: s.TotalCalls, SuccessCalls: s.SuccessCalls, CurrentRPM: s.CurrentRPM,
		ByTool:     s.ByTool,
		HasMore:    s.RecordsPage.HasMore,
		NextCursor: encodeUsageRecordCursor(s.RecordsPage.NextCursor),
	}
	for _, bucket := range s.TrafficBuckets {
		out.TrafficBuckets = append(out.TrafficBuckets, UsageBucketDTO{
			Start: bucket.Start, End: bucket.End, Calls: bucket.Calls,
		})
	}
	for _, r := range s.Records {
		out.Records = append(out.Records, toUsageRecordResponse(r))
	}
	return out
}

func toUsageRecordsResponse(page *store.UsageRecordPage) UsageRecordsResponse {
	response := UsageRecordsResponse{
		Records:    make([]UsageRecordDTO, 0, len(page.Records)),
		NextCursor: encodeUsageRecordCursor(page.NextCursor),
		HasMore:    page.HasMore,
	}
	for _, record := range page.Records {
		response.Records = append(response.Records, toUsageRecordResponse(record))
	}
	return response
}

func toUsageRecordResponse(record store.UsageRecord) UsageRecordDTO {
	return UsageRecordDTO{
		ID: record.ID, KeyID: record.KeyID, ToolName: record.ToolName,
		Timestamp: record.Timestamp, DurationMs: record.DurationMs, Success: record.Success,
		DebugJSON:              record.DebugJSON,
		HasDebugRequestBody:    record.HasDebugRequestBody,
		HasDebugResponseBody:   record.HasDebugResponseBody,
		DebugRequestBodyBytes:  record.DebugRequestBytes,
		DebugResponseBodyBytes: record.DebugResponseBytes,
	}
}

func toUsageRecordDetailResponse(record *store.UsageRecord) UsageRecordDetailDTO {
	return UsageRecordDetailDTO{
		ID: record.ID, KeyID: record.KeyID, ToolName: record.ToolName,
		Timestamp: record.Timestamp, DurationMs: record.DurationMs, Success: record.Success,
		DebugJSON:              record.DebugJSON,
		HasDebugRequestBody:    record.HasDebugRequestBody,
		HasDebugResponseBody:   record.HasDebugResponseBody,
		DebugRequestBodyBytes:  record.DebugRequestBytes,
		DebugResponseBodyBytes: record.DebugResponseBytes,
		DebugRequestBody:       record.DebugRequestBody,
		DebugResponseBody:      record.DebugResponseBody,
	}
}
