// Package usage 在 HTTP MCP 链路上记录 API Key 的调用次数与工具级明细（尽力而为，不阻塞主路径）。
package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
)

// SuccessQuotaReleaser rolls back a previously reserved success call.
// Defined at the consumer side so usage does not require the full store.Store.
type SuccessQuotaReleaser interface {
	ReleaseSuccessCall(ctx context.Context, userID string) error
}

// DebugSettingsReader loads server settings for debug capture decisions.
type DebugSettingsReader interface {
	GetServerSettings(ctx context.Context) (*store.ServerSettings, error)
}

// UsageStore is the minimal store surface needed by MCP usage middleware.
type UsageStore interface {
	SuccessQuotaReleaser
	DebugSettingsReader
}

// toolNameCtxKey 为 context 中存放 tools/call 工具名的私有键类型。
type toolNameCtxKey struct{}

type jsonRPCRequestInspection struct {
	ToolName       string
	IsBatchRequest bool
}

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
// JSON-RPC batch 请求会被提前拒绝，避免批量 tools/call 绕过配额预留与用量记录。
func ExtractToolNameMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inspection := inspectJSONRPCRequest(r)
			if inspection.IsBatchRequest {
				writeJSONRPCBatchUnsupported(w)
				return
			}
			r = r.WithContext(WithToolName(r.Context(), inspection.ToolName))
			next.ServeHTTP(w, r)
		})
	}
}

// PeekToolName 解析 tools/call 工具名但不消费 Body；保留供未挂载 ExtractToolNameMiddleware 的旧链路使用。
func PeekToolName(r *http.Request) string {
	return inspectJSONRPCRequest(r).ToolName
}

func writeJSONRPCBatchUnsupported(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32600,"message":"JSON-RPC batch requests are not supported"},"id":null}`))
}

// responseRecorder 捕获 HTTP 状态码；SSE 流式响应仅保留最后一条 data 行用于判断 isError。
type responseRecorder struct {
	http.ResponseWriter
	status                 int
	lastSSEData            []byte
	jsonCapture            []byte
	debugEnabled           bool
	debugResponseBody      []byte
	debugResponseTruncated bool
}

const maxJSONCapture = 256 << 10

// maxDebugResponseBody 限制 debug 模式下缓存的响应体大小（10 MiB），
// 避免上游返回超大 SSE 流时内存无限增长。
const maxDebugResponseBody = 10 << 20

func (s *responseRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *responseRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	s.captureSSEData(p)
	s.captureDebugResponse(p)
	if len(s.lastSSEData) == 0 {
		room := maxJSONCapture - len(s.jsonCapture)
		if room > 0 {
			captureBytes := p
			if len(captureBytes) > room {
				captureBytes = captureBytes[:room]
			}
			s.jsonCapture = append(s.jsonCapture, captureBytes...)
		}
	}
	return s.ResponseWriter.Write(p)
}

func (s *responseRecorder) captureDebugResponse(p []byte) {
	if !s.debugEnabled || len(p) == 0 {
		return
	}
	room := maxDebugResponseBody - len(s.debugResponseBody)
	if room <= 0 {
		s.debugResponseTruncated = true
		return
	}
	if len(p) > room {
		s.debugResponseBody = append(s.debugResponseBody, p[:room]...)
		s.debugResponseTruncated = true
		return
	}
	s.debugResponseBody = append(s.debugResponseBody, p...)
}

func (s *responseRecorder) captureSSEData(p []byte) {
	for line := range bytes.SplitSeq(p, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) > 0 {
			s.lastSSEData = append([]byte(nil), data...)
		}
	}
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
// API Key 调用数与 usage_log 的 COUNT 口径一致，并避免握手请求（initialize/ping 等）刷新 last_used_at。
// success_calls 在 quota 中间件中已原子预留；此处根据 MCP isError / HTTP 状态回滚失败调用。
// 使用 defer + recover 保证即便下游 handler panic，release/usage 后处理也会执行，避免 success_calls 虚高。
func MCPMiddleware(st UsageStore, writer *store.AsyncUsageWriter) func(http.Handler) http.Handler {
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

			debugEnabled := serverDebugEnabled(r.Context(), st)
			var debugRequestBody []byte
			var debugRequestBodyTruncated bool
			if debugEnabled {
				debugRequestBody, debugRequestBodyTruncated = captureAndRestoreRequestBody(r)
			}

			user, hasUser := auth.UserFromContext(r.Context())
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK, debugEnabled: debugEnabled}

			// recover 捕获 handler panic：将状态视为失败并执行 release 逻辑，
			// 随后重新 panic 让 http.Server 在连接层处理（关闭连接）。
			// success_calls 由 quota 中间件预留，此处必须回滚。
			defer func() {
				if rcv := recover(); rcv != nil {
					if hasUser {
						releaseReservedSuccessCall(st, r.Context(), user.ID)
					}
					panic(rcv)
				}
			}()

			next.ServeHTTP(rec, r)
			dur := time.Since(start).Milliseconds()

			body := rec.lastSSEData
			if len(body) == 0 {
				body = rec.jsonCapture
			}
			httpOK := rec.status >= 200 && rec.status < 300
			mcpOK := httpOK && !mcpToolResultIsError(body)
			success := mcpOK

			if hasUser {
				if !mcpOK {
					releaseReservedSuccessCall(st, r.Context(), user.ID)
				}
			}
			if writer != nil {
				debugJSON := ""
				if debugEnabled {
					debugJSON = buildDebugJSON(r, key, user, hasUser, toolName, start, dur, success, rec, debugRequestBody, debugRequestBodyTruncated)
				}
				writer.Enqueue(store.UsageRecord{KeyID: key.ID, TouchKey: true})
				writer.Enqueue(store.UsageRecord{
					KeyID: key.ID, ToolName: toolName,
					Timestamp: time.Now().UTC(), DurationMs: dur,
					Success: success, DebugJSON: debugJSON,
				})
			}
		})
	}
}

const quotaReleaseTimeout = 2 * time.Second

func releaseReservedSuccessCall(releaser SuccessQuotaReleaser, requestContext context.Context, userID string) {
	if releaser == nil || strings.TrimSpace(userID) == "" {
		return
	}
	releaseContext, cancel := context.WithTimeout(context.WithoutCancel(requestContext), quotaReleaseTimeout)
	defer cancel()
	if err := releaser.ReleaseSuccessCall(releaseContext, userID); err != nil {
		log.Printf("release success quota failed user=%s: %v", userID, err)
	}
}

func serverDebugEnabled(ctx context.Context, reader DebugSettingsReader) bool {
	if reader == nil {
		return false
	}
	settings, err := reader.GetServerSettings(ctx)
	return err == nil && settings != nil && settings.Debug
}

const maxDebugRequestBody = 1 << 20 // 1 MiB

func captureAndRestoreRequestBody(r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDebugRequestBody+1))
	// Restore every byte already read plus the unread tail, so downstream MCP handling
	// still receives the original request body.
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
	if err != nil {
		return []byte("read request body failed: " + err.Error()), false
	}
	if len(body) > maxDebugRequestBody {
		return body[:maxDebugRequestBody], true
	}
	return body, false
}

func buildDebugJSON(r *http.Request, key *store.APIKey, user *auth.AuthenticatedUser, hasUser bool, toolName string, startedAt time.Time, durationMs int64, success bool, rec *responseRecorder, requestBody []byte, requestBodyTruncated bool) string {
	debugPayload := map[string]any{
		"version":     1,
		"captured_at": time.Now().UTC().Format(time.RFC3339Nano),
		"auth": map[string]any{
			"key_id":     key.ID,
			"key_prefix": key.KeyPrefix,
		},
		"mcp": map[string]any{
			"tool_name":   toolName,
			"success":     success,
			"duration_ms": durationMs,
			"started_at":  startedAt.UTC().Format(time.RFC3339Nano),
		},
		"request": map[string]any{
			"method":         r.Method,
			"path":           r.URL.Path,
			"query":          r.URL.RawQuery,
			"remote_addr":    r.RemoteAddr,
			"host":           r.Host,
			"content_length": r.ContentLength,
			"headers":        headerSnapshot(r.Header),
			"body":           string(requestBody),
			"body_truncated": requestBodyTruncated,
		},
		"response": map[string]any{
			"status":         rec.status,
			"headers":        headerSnapshot(rec.Header()),
			"body":           string(rec.debugResponseBody),
			"body_truncated": rec.debugResponseTruncated,
		},
	}
	if hasUser {
		debugPayload["auth"].(map[string]any)["user_id"] = user.ID
	}
	body, err := json.MarshalIndent(debugPayload, "", "  ")
	if err != nil {
		return "{\"error\":\"failed to marshal debug payload\"}"
	}
	return string(body)
}

func headerSnapshot(headers http.Header) map[string][]string {
	out := make(map[string][]string, len(headers))
	for name, values := range headers {
		copiedValues := append([]string(nil), values...)
		if isSensitiveHeader(name) {
			for index := range copiedValues {
				copiedValues[index] = "[redacted]"
			}
		}
		out[name] = copiedValues
	}
	return out
}

func isSensitiveHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key":
		return true
	default:
		return false
	}
}

// mcpToolResultIsError 解析 Streamable HTTP 响应中的 tools/call 是否失败。
// JSON-RPC 顶层 error 与 result.isError 都表示调用失败；支持 application/json
// 单条/批量响应，以及 text/event-stream 中 data: 行内的 JSON-RPC 消息。
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
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false
	}

	jsonRPCError := bytes.TrimSpace(envelope.Error)
	hasJSONRPCError := len(jsonRPCError) > 0 && !bytes.Equal(jsonRPCError, []byte("null"))
	return hasJSONRPCError || envelope.Result.IsError
}

// maxParseBody 限制解析工具名时读入内存的字节数；超出则跳过解析（请求照常放行，Body 完整透传）。
const maxParseBody = 1 << 20 // 1 MiB

// extractToolName 从 MCP JSON-RPC 请求体解析 tools/call 的 params.name。
// 读 Body 时有大小上限，但会用 MultiReader 完整恢复，以便下游 handler 仍能读取全部内容。
func extractToolName(r *http.Request) string {
	return inspectJSONRPCRequest(r).ToolName
}

func inspectJSONRPCRequest(r *http.Request) jsonRPCRequestInspection {
	if r.Body == nil {
		return jsonRPCRequestInspection{}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxParseBody+1))
	if err != nil {
		return jsonRPCRequestInspection{}
	}
	// 恢复完整 Body：已读字节 + 原始流的剩余部分（若 LimitReader 在上限前停止，则后者为空流）。
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))

	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) > 0 && trimmedBody[0] == '[' {
		return jsonRPCRequestInspection{IsBatchRequest: true}
	}

	if int64(len(body)) > maxParseBody {
		return jsonRPCRequestInspection{} // 超出上限的非典型请求体，跳过解析。
	}

	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return jsonRPCRequestInspection{}
	}
	if msg.Method == "tools/call" {
		return jsonRPCRequestInspection{ToolName: msg.Params.Name}
	}
	return jsonRPCRequestInspection{}
}
