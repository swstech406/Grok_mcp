// Package usage 在 HTTP MCP 链路上记录 API Key 的调用次数与工具级明细（尽力而为，不阻塞主路径）。
package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
)

// maxParseBody 限制解析工具名时读入内存的字节数；超出则跳过解析（请求照常放行，Body 完整透传）。
const maxParseBody = 1 << 20 // 1 MiB

// MCPMiddleware 在请求前后统计耗时；仅 tools/call 才计入用量，以保证
// total_calls 与 usage_log 的 COUNT 口径一致，并避免握手请求（initialize/ping 等）刷新 last_used_at。
func MCPMiddleware(st store.Store, writer *store.AsyncUsageWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := auth.APIKeyFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			toolName := extractToolName(r)
			start := time.Now()
			next.ServeHTTP(w, r)
			dur := time.Since(start).Milliseconds()

			if toolName == "" {
				return
			}
			_ = st.TouchKeyUsage(r.Context(), key.ID)
			if writer != nil {
				writer.Enqueue(store.UsageRecord{
					KeyID: key.ID, ToolName: toolName,
					Timestamp: time.Now().UTC(), DurationMs: dur,
				})
			}
		})
	}
}

// extractToolName 从 MCP JSON-RPC 请求体解析 tools/call 的 params.name。
// 读 Body 时有大小上限，但会用 MultiReader 完整恢复，以便下游 handler 仍能读取全部内容。
func extractToolName(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxParseBody+1))
	if err != nil {
		return ""
	}
	// 恢复完整 Body：已读字节 + 原始流的剩余部分（若 LimitReader 在上限前停止，则后者为空流）。
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))

	if int64(len(body)) > maxParseBody {
		return "" // 超出上限的非典型请求体，跳过解析。
	}

	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return ""
	}
	if msg.Method == "tools/call" {
		return msg.Params.Name
	}
	return ""
}
