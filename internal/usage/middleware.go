// Package usage 在 HTTP MCP 链路上记录 API Key 的调用次数与工具级明细（尽力而为，不阻塞主路径）。
package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

// SuccessQuotaReleaser rolls back a previously reserved success call.
// Defined at the consumer side so usage does not require the full store.Store.
type SuccessQuotaReleaser interface {
	ReleaseSuccessCall(ctx context.Context, reservation store.SuccessQuotaReservation) error
}

// DebugState exposes the runtime debug switch without loading persisted settings.
type DebugState interface {
	Enabled() bool
}

// UsageStore is the minimal store surface needed by MCP usage middleware.
type UsageStore interface {
	SuccessQuotaReleaser
}

// toolNameCtxKey 为 context 中存放 tools/call 工具名的私有键类型。
type toolNameCtxKey struct{}

// successQuotaReservationCtxKey keeps the store-selected accounting bucket
// private while allowing quota middleware to pass it to failure cleanup.
type successQuotaReservationCtxKey struct{}

// searchPermitReleaseCtxKey stores an idempotent callback supplied by the
// search-concurrency middleware. Failed requests can release scarce search
// capacity before waiting for the synchronous quota rollback write.
type searchPermitReleaseCtxKey struct{}

// WithSearchPermitRelease attaches a callback that releases the current
// search-concurrency permit. The callback is intentionally generic so the
// usage middleware does not depend on the ratelimit package.
func WithSearchPermitRelease(ctx context.Context, release func()) context.Context {
	if release == nil {
		return ctx
	}
	return context.WithValue(ctx, searchPermitReleaseCtxKey{}, release)
}

func releaseSearchPermitBeforeQuotaRollback(ctx context.Context) {
	release, ok := ctx.Value(searchPermitReleaseCtxKey{}).(func())
	if !ok || release == nil {
		return
	}
	release()
}

type jsonRPCRequestInspection struct {
	ToolName                      string
	IsBatchRequest                bool
	HasAmbiguousToolRoutingFields bool
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

// WithSuccessQuotaReservation attaches a successfully persisted quota
// reservation for exact rollback by downstream usage middleware.
func WithSuccessQuotaReservation(ctx context.Context, reservation store.SuccessQuotaReservation) context.Context {
	if !isUsableSuccessQuotaReservation(reservation) {
		return ctx
	}
	return context.WithValue(ctx, successQuotaReservationCtxKey{}, reservation)
}

// SuccessQuotaReservationFromContext returns the exact store reservation that
// belongs to the current request. Invalid values are treated as absent.
func SuccessQuotaReservationFromContext(ctx context.Context) (store.SuccessQuotaReservation, bool) {
	if ctx == nil {
		return store.SuccessQuotaReservation{}, false
	}
	reservation, ok := ctx.Value(successQuotaReservationCtxKey{}).(store.SuccessQuotaReservation)
	if !ok || !isUsableSuccessQuotaReservation(reservation) {
		return store.SuccessQuotaReservation{}, false
	}
	return reservation, true
}

func isUsableSuccessQuotaReservation(reservation store.SuccessQuotaReservation) bool {
	return reservation.IsValid()
}

// ExtractToolNameMiddleware 在鉴权之后、额度与用量中间件之前解析一次 tools/call 工具名并写入 context。
// 这样后续中间件直接读取 context，无需重复读取并恢复 Body。对非 tools/call 请求写入空名后透传。
// JSON-RPC batch、重复路由字段和大小写碰撞别名会被提前拒绝，避免 SDK 与限额层产生不同的工具识别结果。
func ExtractToolNameMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inspection := inspectJSONRPCRequest(r)
			if inspection.IsBatchRequest {
				writeJSONRPCBatchUnsupported(w)
				return
			}
			if inspection.HasAmbiguousToolRoutingFields {
				writeJSONRPCAmbiguousToolRoutingFields(w)
				return
			}
			r = r.WithContext(WithToolName(r.Context(), inspection.ToolName))
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSONRPCBatchUnsupported(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32600,"message":"JSON-RPC batch requests are not supported"},"id":null}`))
}

func writeJSONRPCAmbiguousToolRoutingFields(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32600,"message":"Ambiguous JSON-RPC tool routing fields are not supported"},"id":null}`))
}

// responseRecorder captures HTTP status, incrementally inspects a bounded
// prefix for protocol-level failures, and streams debug bytes to disk.
type responseRecorder struct {
	http.ResponseWriter
	status           int
	outcomeInspector responseOutcomeInspector
	responseSpool    *debugBodySpool
}

func (s *responseRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *responseRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	s.outcomeInspector.inspect(p)
	s.responseSpool.write(p)
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
// API Key 调用数与 usage_log 的 COUNT 口径一致，并避免握手请求（initialize/ping 等）刷新 last_used_at。
// success_calls 在 quota 中间件中已原子预留；此处根据 MCP isError / HTTP 状态回滚失败调用。
// 使用 defer + recover 保证即便下游 handler panic，release/usage 后处理也会执行，避免 success_calls 虚高。
func MCPMiddleware(st UsageStore, writer *store.AsyncUsageWriter, debugStates ...DebugState) func(http.Handler) http.Handler {
	var debugState DebugState
	if len(debugStates) > 0 {
		debugState = debugStates[0]
	}
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
				inspection := inspectJSONRPCRequest(r)
				if inspection.IsBatchRequest {
					writeJSONRPCBatchUnsupported(w)
					return
				}
				if inspection.HasAmbiguousToolRoutingFields {
					writeJSONRPCAmbiguousToolRoutingFields(w)
					return
				}
				toolName = inspection.ToolName
			}
			if toolName == "" {
				next.ServeHTTP(w, r)
				return
			}
			user, hasUser := auth.UserFromContext(r.Context())
			reservation, hasReservation := SuccessQuotaReservationFromContext(r.Context())
			if hasReservation && (!hasUser || reservation.UserID != user.ID) {
				hasReservation = false
			}

			debugEnabled := serverDebugEnabled(debugState)
			var debugCapture *debugCaptureSession
			if debugEnabled {
				debugCapture = startDebugCapture(r)
			}
			captureTransferred := false

			start := time.Now()
			outcomeContext, outcomeMarker := WithToolOutcomeMarker(r.Context())
			r = r.WithContext(outcomeContext)
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			if debugCapture != nil {
				rec.responseSpool = debugCapture.responseSpool
			}
			quotaReleaseAttempted := false
			releaseQuotaOnce := func() {
				if !hasReservation || quotaReleaseAttempted {
					return
				}
				quotaReleaseAttempted = true
				releaseReservedSuccessCall(st, r.Context(), reservation)
			}

			// recover 捕获 handler panic：将状态视为失败并执行 release 逻辑，
			// 随后重新 panic 让 http.Server 在连接层处理（关闭连接）。
			// success_calls 由 quota 中间件预留，此处必须回滚。
			defer func() {
				if debugCapture != nil {
					debugCapture.finalize()
					if !captureTransferred {
						debugCapture.cleanup()
					}
				}
				if rcv := recover(); rcv != nil {
					releaseQuotaOnce()
					panic(rcv)
				}
			}()

			next.ServeHTTP(rec, r)
			dur := time.Since(start).Milliseconds()
			if debugCapture != nil {
				debugCapture.finalize()
			}

			httpOK := rec.status >= 200 && rec.status < 300
			semanticSuccess, semanticOutcomeKnown := outcomeMarker.Outcome()
			mcpOK := httpOK
			if semanticOutcomeKnown {
				mcpOK = mcpOK && semanticSuccess
			} else {
				mcpOK = mcpOK && !rec.outcomeInspector.toolError()
			}
			success := mcpOK

			if !mcpOK {
				releaseQuotaOnce()
			}
			if writer != nil {
				debugJSON := ""
				if debugEnabled {
					debugJSON = buildDebugJSON(r, key, user, hasUser, toolName, start, dur, success, rec, debugCapture)
				}
				usageRecord := store.UsageRecord{
					KeyID: key.ID, ToolName: toolName,
					Timestamp: time.Now().UTC(), DurationMs: dur,
					Success: success, DebugJSON: debugJSON,
				}
				if debugCapture != nil {
					usageRecord.DebugRequestBodyPath = debugCapture.requestPath()
					usageRecord.DebugResponseBodyPath = debugCapture.responsePath()
					usageRecord.DebugRequestObservedBytes = debugCapture.requestObservedBytes()
					usageRecord.DebugResponseObservedBytes = debugCapture.responseObservedBytes()
					usageRecord.DebugRequestTruncated = debugCapture.requestTruncated()
					usageRecord.DebugResponseTruncated = debugCapture.responseTruncated()
					usageRecord.Cleanup = debugCapture.cleanup
					captureTransferred = true
				}
				writer.Enqueue(usageRecord)
			}
		})
	}
}

const quotaReleaseTimeout = 2 * time.Second

func releaseReservedSuccessCall(releaser SuccessQuotaReleaser, requestContext context.Context, reservation store.SuccessQuotaReservation) {
	if releaser == nil || !isUsableSuccessQuotaReservation(reservation) {
		return
	}
	// Search capacity is scarcer than the quota write path. Release it before
	// the bounded rollback so a slow SQLite write does not retain a search slot.
	releaseSearchPermitBeforeQuotaRollback(requestContext)
	releaseContext, cancel := context.WithTimeout(context.WithoutCancel(requestContext), quotaReleaseTimeout)
	defer cancel()
	if err := releaser.ReleaseSuccessCall(releaseContext, reservation); err != nil {
		log.Printf("release success quota failed user=%s period=%s error_type=%T", reservation.UserID, reservation.Period, err)
	}
}

func serverDebugEnabled(debugState DebugState) bool {
	return debugState != nil && debugState.Enabled()
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

func buildDebugJSON(r *http.Request, key *store.APIKey, user *auth.AuthenticatedUser, hasUser bool, toolName string, startedAt time.Time, durationMs int64, success bool, rec *responseRecorder, capture *debugCaptureSession) string {
	debugPayload := map[string]any{
		"version":     2,
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
			"method":              r.Method,
			"path":                r.URL.Path,
			"query":               r.URL.RawQuery,
			"remote_addr":         r.RemoteAddr,
			"host":                r.Host,
			"content_length":      r.ContentLength,
			"headers":             headerSnapshot(r.Header),
			"body_bytes":          capture.requestBytes(),
			"body_observed_bytes": capture.requestObservedBytes(),
			"body_truncated":      capture.requestTruncated(),
			"body_storage":        "debug_sqlite",
		},
		"response": map[string]any{
			"status":              rec.status,
			"headers":             headerSnapshot(rec.Header()),
			"body_bytes":          capture.responseBytes(),
			"body_observed_bytes": capture.responseObservedBytes(),
			"body_truncated":      capture.responseTruncated(),
			"body_storage":        "debug_sqlite",
		},
	}
	if captureError := capture.captureError(); captureError != "" {
		debugPayload["capture_error"] = captureError
	}
	if hasUser {
		debugPayload["auth"].(map[string]any)["user_id"] = user.ID
	}
	body, err := json.Marshal(debugPayload)
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
	case "authorization",
		"proxy-authorization",
		"cookie",
		"set-cookie",
		"api-key",
		"apikey",
		"x-api-key",
		"x-auth-token",
		"x-access-token",
		"x-csrf-token",
		"x-xsrf-token",
		"x-amz-security-token",
		"x-goog-api-key":
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

	// Parse routing fields without decoding into a tagged struct. encoding/json
	// matches struct fields case-insensitively, while the downstream MCP SDK uses
	// exact protocol field names. Duplicate object-valued fields can also merge
	// differently across decoders, so reject both duplicates and case aliases.
	envelope, hasAmbiguousEnvelopeFields, err := inspectCaseSensitiveJSONObject(body, "method", "params")
	if err != nil {
		return jsonRPCRequestInspection{}
	}
	if hasAmbiguousEnvelopeFields {
		return jsonRPCRequestInspection{HasAmbiguousToolRoutingFields: true}
	}

	methodJSON, hasMethod := envelope["method"]
	if !hasMethod {
		return jsonRPCRequestInspection{}
	}
	var method string
	if err := json.Unmarshal(methodJSON, &method); err != nil || method != "tools/call" {
		return jsonRPCRequestInspection{}
	}

	paramsJSON, hasParams := envelope["params"]
	if !hasParams {
		return jsonRPCRequestInspection{}
	}
	params, hasAmbiguousParamsFields, err := inspectCaseSensitiveJSONObject(paramsJSON, "name")
	if err != nil {
		return jsonRPCRequestInspection{}
	}
	if hasAmbiguousParamsFields {
		return jsonRPCRequestInspection{HasAmbiguousToolRoutingFields: true}
	}

	nameJSON, hasName := params["name"]
	if !hasName {
		return jsonRPCRequestInspection{}
	}
	var toolName string
	if err := json.Unmarshal(nameJSON, &toolName); err != nil {
		return jsonRPCRequestInspection{}
	}
	return jsonRPCRequestInspection{ToolName: toolName}
}

func inspectCaseSensitiveJSONObject(body []byte, routingFieldNames ...string) (map[string]json.RawMessage, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	openingToken, err := decoder.Token()
	if err != nil {
		return nil, false, err
	}
	openingDelimiter, isDelimiter := openingToken.(json.Delim)
	if !isDelimiter || openingDelimiter != '{' {
		return nil, false, errors.New("JSON value is not an object")
	}

	exactValues := make(map[string]json.RawMessage, len(routingFieldNames))
	seenRoutingFields := make(map[string]bool, len(routingFieldNames))
	hasAmbiguousRoutingFields := false
	for decoder.More() {
		fieldToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, false, tokenErr
		}
		fieldName, isString := fieldToken.(string)
		if !isString {
			return nil, false, errors.New("JSON object key is not a string")
		}

		var rawValue json.RawMessage
		if decodeErr := decoder.Decode(&rawValue); decodeErr != nil {
			return nil, false, decodeErr
		}
		for _, canonicalName := range routingFieldNames {
			if !strings.EqualFold(fieldName, canonicalName) {
				continue
			}
			if fieldName != canonicalName || seenRoutingFields[canonicalName] {
				hasAmbiguousRoutingFields = true
			}
			seenRoutingFields[canonicalName] = true
			if fieldName == canonicalName {
				exactValues[canonicalName] = rawValue
			}
			break
		}
	}

	closingToken, err := decoder.Token()
	if err != nil {
		return nil, false, err
	}
	closingDelimiter, isDelimiter := closingToken.(json.Delim)
	if !isDelimiter || closingDelimiter != '}' {
		return nil, false, errors.New("JSON object is not properly closed")
	}
	if trailingErr := decoder.Decode(&struct{}{}); trailingErr != io.EOF {
		if trailingErr == nil {
			return nil, false, errors.New("multiple JSON values are not supported")
		}
		return nil, false, trailingErr
	}
	return exactValues, hasAmbiguousRoutingFields, nil
}
