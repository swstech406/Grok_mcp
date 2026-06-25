// Package quota 在 MCP tools/call 前按用户汇总额度原子预留 total_calls 与 success_calls。
package quota

import (
	"errors"
	"net/http"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/usage"
)

// MCPMiddleware 仅对 tools/call 在 handler 前原子预留用户 total_calls 与 success_calls。
func MCPMiddleware(st store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 优先复用链路前置 ExtractToolNameMiddleware 写入的工具名；
			// 兼容未挂载该中间件的旧用法，回退到一次解析。
			toolName, ok := usage.ToolNameFromContext(r.Context())
			if !ok {
				toolName = usage.PeekToolName(r)
			}
			if toolName == "" {
				next.ServeHTTP(w, r)
				return
			}
			user, ok := auth.UserFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if err := st.ReserveTotalCall(r.Context(), user.ID, user.TotalLimit); err != nil {
				if errors.Is(err, store.ErrQuotaTotal) {
					http.Error(w, "total request limit exceeded", http.StatusTooManyRequests)
					return
				}
				http.Error(w, "quota check failed", http.StatusInternalServerError)
				return
			}
			if err := st.ReserveSuccessCall(r.Context(), user.ID, user.SuccessLimit); err != nil {
				_ = st.ReleaseTotalCall(r.Context(), user.ID)
				if errors.Is(err, store.ErrQuotaSuccess) {
					http.Error(w, "success request limit exceeded", http.StatusTooManyRequests)
					return
				}
				http.Error(w, "quota check failed", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}