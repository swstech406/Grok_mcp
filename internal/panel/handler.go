package panel

import (
	"context"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

// Handler 实现面板 API；路由由 NewMux 注册。
type Handler struct {
	Store                 store.Store
	JWTSecret             string
	InitialServerSettings config.ServerSettings
	SettingsApplier       ServerSettingsApplier      // 可选；保存服务器设置后热更新运行时组件
	ModelLister           ModelLister                // 可选；管理员面板通过它从上游拉取可用 Grok 模型
	AuthCache             AuthCacheInvalidator       // 可选；管理员变更用户/等级/密钥后清空 MCP 鉴权缓存
	AuthProtector         *AuthProtector             // 可选；未设置时使用内置面板登录/注册防护
	SQLiteMetrics         SQLiteMetricsProvider      // 可选；管理员运行指标中的 SQLite 快照
	UsageWriterMetrics    UsageWriterMetricsProvider // 可选；管理员运行指标中的异步队列快照
}

// ServerSettingsApplier 接收热更新后的服务器运行时设置。
type ServerSettingsApplier interface {
	ApplyServerSettings(config.ServerSettings) error
}

// ModelLister fetches the currently available upstream Grok models.
type ModelLister interface {
	ListModels(context.Context) ([]grok.Model, error)
}

// AuthCacheInvalidator 在管理员变更 tier/用户/密钥后清空 MCP 鉴权缓存。
type AuthCacheInvalidator interface {
	InvalidateAll()
}

// SQLiteMetricsProvider exposes a non-sensitive snapshot without widening the
// persistence Store interface used by unrelated panel and authentication code.
type SQLiteMetricsProvider interface {
	SQLiteMetrics() store.SQLiteMetricsSnapshot
}

// UsageWriterMetricsProvider exposes the asynchronous writer's lock-free
// counters and queue state.
type UsageWriterMetricsProvider interface {
	Stats() store.AsyncUsageWriterStats
}
