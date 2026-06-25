// Package usage 在 HTTP MCP 链路上记录 API Key 的调用次数与工具级明细（尽力而为，不阻塞主路径）。
package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
)

// toolNameCtxKey 为 context 中存放 tools/call 工具名的私有键类型。
type toolNameCtxKey struct{}

// WithToolName 将已解析的工具名附加到 context，供下游中间件复用，避免重复解析请求体。
func WithToolName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, toolNameCtxKey{}, name)
}

// ToolNameFromContext 返回链路前置中间件提取的工具名；第二个值为 false 表示未提取过。
func ToolNameFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(toolNameCtxKey{}).(string)
	return v, ok
}

// ExtractToolNameMiddleware 在鉴权之后、额度与用量中间件之前解析一次 tools/call 工具名并写入 context。
// 这样后续中间件直接读取 context，无需重复读取并恢复 Body。对非 tools/call 请求写入空名后透传。
func ExtractToolNameMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := extractToolName(r)
			r = r.WithContext(WithToolName(r.Context(), name))
			next.ServeHTTP(w, r)
		})
	}
}

// PeekToolName 解析 tools/call 工具名但不消费 Body；保留供未挂载 ExtractToolNameMiddleware 的旧链路使用。
func PeekToolName(r *http.Request) string {
	return extractToolName(r)
}

// responseRecorder 捕获 HTTP 状态码与响应体，用于判断 MCP tools/call 是否成功。
type responseRecorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (s *responseRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *responseRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	s.buf.Write(p)
	return s.ResponseWriter.Write(p)
}

func (s *responseRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *responseRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// MCPMiddleware 在请求前后统计耗时；仅 tools/call 才计入用量，以保证
// total_calls 与 usage_log 的 COUNT 口径一致，并避免握手请求（initialize/ping 等）刷新 last_used_at。
// 成功次数在 quota 中间件中已原子预留；此处根据 MCP isError / HTTP 状态回滚失败调用。
// 使用 defer + recover 保证即便下游 handler panic，release/usage 后处理也会执行，避免 success_calls 虚高。
func MCPMiddleware(st store.Store, writer *store.AsyncUsageWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := auth.APIKeyFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			// 优先复用链路前置 ExtractToolNameMiddleware 写入的工具名；
			// 兼容直接调用 MCPMiddleware（未挂载提取中间件）的旧用法，回退到一次解析。
			toolName, ok := ToolNameFromContext(r.Context())
			if !ok {
				toolName = extractToolName(r)
			}
			if toolName == "" {
				next.ServeHTTP(w, r)
				return
			}

			user, hasUser := auth.UserFromContext(r.Context())
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

			// recover 捕获 handler panic：将状态视为失败并执行 release 逻辑，
			// 随后重新 panic 让 http.Server 在连接层处理（关闭连接）。
			defer func() {
				if rcv := recover(); rcv != nil {
					if hasUser {
						_ = st.ReleaseSuccessCall(r.Context(), user.ID)
					}
					panic(rcv)
				}
			}()

			next.ServeHTTP(rec, r)
			dur := time.Since(start).Milliseconds()

			httpOK := rec.status >= 200 && rec.status < 300
			mcpOK := httpOK && !mcpToolResultIsError(rec.buf.Bytes())
			success := mcpOK

			_ = st.TouchKeyUsage(r.Context(), key.ID)
			if hasUser {
				if !mcpOK {
					_ = st.ReleaseSuccessCall(r.Context(), user.ID)
				}
			}
			if writer != nil {
				writer.Enqueue(store.UsageRecord{
					KeyID: key.ID, ToolName: toolName,
					Timestamp: time.Now().UTC(), DurationMs: dur,
					Success: success,
				})
			}
		})
	}
}

// mcpToolResultIsError 解析 Streamable HTTP 响应中的 tools/call 结果是否带 isError。
// 支持 application/json 单条/批量响应，以及 text/event-stream 中 data: 行内的 JSON-RPC 消息。
func mcpToolResultIsError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	trim := bytes.TrimSpace(body)
	if len(trim) == 0 {
		return false
	}
	if trim[0] == '{' || trim[0] == '[' {
		return jsonRPCPayloadHasToolError(trim)
	}
	for _, payload := range sseDataPayloads(trim) {
		if jsonRPCPayloadHasToolError(payload) {
			return true
		}
	}
	return false
}

func jsonRPCPayloadHasToolError(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return false
	}
	if payload[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(payload, &batch); err != nil {
			return false
		}
		for _, item := range batch {
			if toolCallResultIsError(item) {
				return true
			}
		}
		return false
	}
	return toolCallResultIsError(payload)
}

func sseDataPayloads(body []byte) [][]byte {
	var out [][]byte
	for line := range bytes.SplitSeq(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) > 0 {
			out = append(out, data)
		}
	}
	return out
}

func toolCallResultIsError(raw json.RawMessage) bool {
	var envelope struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false
	}
	return envelope.Result.IsError
}

// maxParseBody 限制解析工具名时读入内存的字节数；超出则跳过解析（请求照常放行，Body 完整透传）。
const maxParseBody = 1 << 20 // 1 MiB

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