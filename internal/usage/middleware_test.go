package usage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
)

// fakeStore 的 recordedUsage 字段会被 AsyncUsageWriter 后台 goroutine 写入、
// 测试主 goroutine 读取，因此用 mutex 保护以避免数据竞争。
type fakeStore struct {
	store.TestStore
	mu            sync.Mutex
	recordedUsage []store.UsageRecord
}

func (f *fakeStore) RecordUsage(_ context.Context, record store.UsageRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordedUsage = append(f.recordedUsage, record)
	return nil
}

func (f *fakeStore) ReleaseSuccessCall(context.Context, string) error { return nil }

func (f *fakeStore) TryIncrementUserSuccessCalls(context.Context, string, int) error {
	return nil
}

func (f *fakeStore) RecordedUsage() []store.UsageRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.UsageRecord(nil), f.recordedUsage...)
}

func TestMCPMiddlewareGatesUsageByToolCall(t *testing.T) {
	key := &store.APIKey{ID: "k1"}
	user := &auth.AuthenticatedUser{User: store.User{ID: "u1"}, SuccessLimit: 0}
	st := &fakeStore{}
	writer := store.NewAsyncUsageWriter(st, 8)
	defer writer.Close()
	h := MCPMiddleware(st, writer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"initialize"}`))
	req = req.WithContext(auth.WithAPIKey(req.Context(), key))
	req = req.WithContext(auth.WithUser(req.Context(), user))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if recordedUsage := st.RecordedUsage(); len(recordedUsage) != 0 {
		t.Fatalf("initialize must not record usage, got records=%d", len(recordedUsage))
	}

	req2 := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"grok_web_search"}}`))
	req2 = req2.WithContext(auth.WithAPIKey(req2.Context(), key))
	req2 = req2.WithContext(auth.WithUser(req2.Context(), user))
	h.ServeHTTP(httptest.NewRecorder(), req2)
	deadline := time.Now().Add(2 * time.Second)
	for len(st.RecordedUsage()) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	recordedUsage := st.RecordedUsage()
	if len(recordedUsage) != 1 {
		t.Fatalf("tools/call should enqueue one usage record, got %d", len(recordedUsage))
	}
	if recordedUsage[0].KeyID != "k1" || recordedUsage[0].ToolName != "grok_web_search" {
		t.Fatalf("unexpected usage record: key=%q tool=%q", recordedUsage[0].KeyID, recordedUsage[0].ToolName)
	}
}

func TestExtractToolNameParsesAndRestoresBody(t *testing.T) {
	payload := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"grok_x_search"}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	if name := extractToolName(r); name != "grok_x_search" {
		t.Fatalf("expected grok_x_search, got %q", name)
	}
	rest, _ := io.ReadAll(r.Body)
	if string(rest) != payload {
		t.Fatalf("body not restored for downstream: got %q", rest)
	}
}

func TestExtractToolNameMiddlewareRejectsJSONRPCBatch(t *testing.T) {
	payload := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"grok_web_search"}}]`
	called := false
	h := ExtractToolNameMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	h.ServeHTTP(rec, req)

	if called {
		t.Fatal("JSON-RPC batch request must not reach downstream handler")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for JSON-RPC batch, got %d", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", contentType)
	}
	if body := rec.Body.String(); !strings.Contains(body, "JSON-RPC batch requests are not supported") {
		t.Fatalf("expected batch rejection message, got %q", body)
	}
}

func TestExtractToolNameIgnoresNonToolCall(t *testing.T) {
	for _, payload := range []string{
		`{"jsonrpc":"2.0","method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"tools/list"}`,
		`not json at all`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
		if name := extractToolName(r); name != "" {
			t.Fatalf("expected empty tool name for %q, got %q", payload, name)
		}
	}
}

func TestExtractToolNameOversizedBodyStillRestored(t *testing.T) {
	big := strings.Repeat("x", maxParseBody+10)
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(big))
	if name := extractToolName(r); name != "" {
		t.Fatalf("expected empty for oversized body, got %q", name)
	}
	rest, _ := io.ReadAll(r.Body)
	if len(rest) != len(big) {
		t.Fatalf("oversized body must be fully restored downstream: got %d want %d", len(rest), len(big))
	}
}

func TestMCPToolResultIsError(t *testing.T) {
	testCases := []struct {
		name         string
		responseBody string
		expectsError bool
	}{
		{
			name:         "successful result",
			responseBody: `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`,
		},
		{
			name:         "successful result containing error text",
			responseBody: `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"the word error is not a JSON-RPC error"}]}}`,
		},
		{
			name:         "tool result isError",
			responseBody: `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"fail"}]}}`,
			expectsError: true,
		},
		{
			name:         "unknown tool JSON-RPC error",
			responseBody: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown tool"}}`,
			expectsError: true,
		},
		{
			name:         "invalid parameters JSON-RPC error",
			responseBody: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`,
			expectsError: true,
		},
		{
			name:         "null JSON-RPC error",
			responseBody: `{"jsonrpc":"2.0","id":1,"error":null,"result":{"content":[]}}`,
		},
		{
			name:         "JSON-RPC error in SSE payload",
			responseBody: "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32602,\"message\":\"Invalid params\"}}\r\n\r\n",
			expectsError: true,
		},
		{
			name:         "JSON-RPC error in batch payload",
			responseBody: `[{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}},{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"unknown tool"}}]`,
			expectsError: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actualError := mcpToolResultIsError([]byte(testCase.responseBody))
			if actualError != testCase.expectsError {
				t.Fatalf("mcpToolResultIsError() = %t, want %t", actualError, testCase.expectsError)
			}
		})
	}
}

func TestResponseOutcomeInspectorHandlesFragmentedSSEAndLatchesError(t *testing.T) {
	inspector := &responseOutcomeInspector{}
	fragments := []string{
		"event: message\r\nda",
		"ta: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"isErr",
		"or\":true}}\r\n\r\n",
		"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n\n",
	}
	for _, fragment := range fragments {
		inspector.inspect([]byte(fragment))
	}
	if !inspector.toolError() {
		t.Fatal("fragmented SSE error must remain latched after a later success event")
	}
}

func TestResponseOutcomeInspectorEnforcesIndependentCap(t *testing.T) {
	inspector := &responseOutcomeInspector{}
	inspector.inspect(bytes.Repeat([]byte("x"), maxOutcomeInspectionBytes+1024))
	inspector.inspect([]byte("\ndata: {\"jsonrpc\":\"2.0\",\"error\":{\"code\":-32603}}\n\n"))

	if inspector.inspectedBytes != maxOutcomeInspectionBytes {
		t.Fatalf("inspected bytes = %d, want cap %d", inspector.inspectedBytes, maxOutcomeInspectionBytes)
	}
	if len(inspector.jsonCapture) > maxOutcomeInspectionBytes || len(inspector.sseLineBuffer) > maxOutcomeInspectionBytes {
		t.Fatalf("inspection buffers exceeded cap: json=%d line=%d", len(inspector.jsonCapture), len(inspector.sseLineBuffer))
	}
	if inspector.toolError() {
		t.Fatal("error arriving after the inspection cap must not be parsed")
	}
}

func TestResponseRecorderFlushDelegates(t *testing.T) {
	var flushed bool
	inner := &flushRecorder{flushed: &flushed}
	rec := &responseRecorder{ResponseWriter: inner}
	rec.Flush()
	if !flushed {
		t.Fatal("expected Flush to delegate to underlying ResponseWriter")
	}
}

type flushRecorder struct {
	http.ResponseWriter
	flushed *bool
}

func (f *flushRecorder) Flush() {
	*f.flushed = true
}

// releaseCountingStore 记录 ReleaseSuccessCall 调用次数，用于断言 panic 时的回滚行为。
type releaseCountingStore struct {
	store.TestStore
	releaseSuccessCalls int
}

func (r *releaseCountingStore) ReleaseSuccessCall(context.Context, string) error {
	r.releaseSuccessCalls++
	return nil
}

type releaseContextRecordingStore struct {
	store.TestStore
	releaseSuccessCalls int
	releaseContextErr   error
}

func (r *releaseContextRecordingStore) ReleaseSuccessCall(ctx context.Context, _ string) error {
	r.releaseSuccessCalls++
	r.releaseContextErr = ctx.Err()
	return nil
}

type failureRecordingStore struct {
	store.TestStore
	releaseSuccessCalls int
	recordedUsage       []store.UsageRecord
}

type debugCaptureRecordingStore struct {
	store.TestStore
	mu                  sync.Mutex
	recordedUsage       []store.UsageRecord
	requestBody         []byte
	responseBody        []byte
	requestPermissions  os.FileMode
	responsePermissions os.FileMode
}

func (s *debugCaptureRecordingStore) Enabled() bool {
	return true
}

func (s *debugCaptureRecordingStore) TouchKeyUsage(context.Context, string) error {
	return nil
}

func (s *debugCaptureRecordingStore) RecordUsage(_ context.Context, record store.UsageRecord) error {
	requestBody, err := os.ReadFile(record.DebugRequestBodyPath)
	if err != nil {
		return err
	}
	responseBody, err := os.ReadFile(record.DebugResponseBodyPath)
	if err != nil {
		return err
	}
	requestInfo, err := os.Stat(record.DebugRequestBodyPath)
	if err != nil {
		return err
	}
	responseInfo, err := os.Stat(record.DebugResponseBodyPath)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordedUsage = append(s.recordedUsage, record)
	s.requestBody = requestBody
	s.responseBody = responseBody
	s.requestPermissions = requestInfo.Mode().Perm()
	s.responsePermissions = responseInfo.Mode().Perm()
	return nil
}

func (s *debugCaptureRecordingStore) snapshot() (store.UsageRecord, []byte, []byte, os.FileMode, os.FileMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordedUsage[0], s.requestBody, s.responseBody, s.requestPermissions, s.responsePermissions
}

func TestMCPMiddlewareBoundsDebugBodiesWithoutChangingForwardedContent(t *testing.T) {
	requestBody := strings.Repeat("request-body-segment|", 120000)
	responseBody := strings.Repeat("response-body-segment|", 550000)
	debugStore := &debugCaptureRecordingStore{}
	writer := store.NewAsyncUsageWriter(debugStore, 4)

	handler := MCPMiddleware(debugStore, writer, debugStore)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		MarkToolOutcome(r.Context(), true)
		_, _ = io.WriteString(w, responseBody)
	}))

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(requestBody))
	requestContext := auth.WithAPIKey(request.Context(), &store.APIKey{ID: "debug-key", KeyPrefix: "grok_dbg"})
	requestContext = WithToolName(requestContext, "grok_web_search")
	request = request.WithContext(requestContext)
	responseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(responseRecorder, request)
	writer.Close()

	if len(debugStore.recordedUsage) != 1 {
		t.Fatalf("recorded usage count = %d, want 1", len(debugStore.recordedUsage))
	}
	record, capturedRequest, capturedResponse, requestPermissions, responsePermissions := debugStore.snapshot()
	if record.DebugRequestBody != "" || record.DebugResponseBody != "" {
		t.Fatalf("queued usage record retained body strings: request=%d response=%d", len(record.DebugRequestBody), len(record.DebugResponseBody))
	}
	if len(record.DebugJSON) > 16<<10 {
		t.Fatalf("debug metadata length = %d, want compact metadata", len(record.DebugJSON))
	}
	if strings.Contains(record.DebugJSON, "request-body-segment") || strings.Contains(record.DebugJSON, "response-body-segment") {
		t.Fatal("debug metadata must not embed request or response bodies")
	}
	if !bytes.Equal(capturedRequest, []byte(requestBody[:maxDebugCapturedBodyBytes])) {
		t.Fatalf("captured request length = %d, want bounded prefix %d", len(capturedRequest), maxDebugCapturedBodyBytes)
	}
	if !bytes.Equal(capturedResponse, []byte(responseBody[:maxDebugCapturedBodyBytes])) {
		t.Fatalf("captured response length = %d, want bounded prefix %d", len(capturedResponse), maxDebugCapturedBodyBytes)
	}
	if record.DebugRequestObservedBytes != int64(len(requestBody)) || record.DebugResponseObservedBytes != int64(len(responseBody)) {
		t.Fatalf("observed bytes request=%d response=%d, want %d and %d", record.DebugRequestObservedBytes, record.DebugResponseObservedBytes, len(requestBody), len(responseBody))
	}
	if !record.DebugRequestTruncated || !record.DebugResponseTruncated {
		t.Fatalf("truncation flags request=%v response=%v, want true", record.DebugRequestTruncated, record.DebugResponseTruncated)
	}
	if responseRecorder.Body.String() != responseBody {
		t.Fatalf("forwarded response length = %d, want %d", responseRecorder.Body.Len(), len(responseBody))
	}
	if requestPermissions != 0o600 || responsePermissions != 0o600 {
		t.Fatalf("spool permissions request=%#o response=%#o, want 0600", requestPermissions, responsePermissions)
	}
	if _, err := os.Stat(record.DebugRequestBodyPath); !os.IsNotExist(err) {
		t.Fatalf("request spool was not removed after persistence: %v", err)
	}
	if _, err := os.Stat(record.DebugResponseBodyPath); !os.IsNotExist(err) {
		t.Fatalf("response spool was not removed after persistence: %v", err)
	}
}

func TestMCPMiddlewarePrefersAuthoritativeSemanticOutcome(t *testing.T) {
	testCases := []struct {
		name                 string
		semanticSuccess      bool
		responseBody         string
		expectedSuccess      bool
		expectedQuotaRelease int
	}{
		{
			name:                 "handler success overrides fallback error payload",
			semanticSuccess:      true,
			responseBody:         `{"jsonrpc":"2.0","id":1,"result":{"isError":true}}`,
			expectedSuccess:      true,
			expectedQuotaRelease: 0,
		},
		{
			name:                 "handler error overrides fallback success payload",
			semanticSuccess:      false,
			responseBody:         `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`,
			expectedSuccess:      false,
			expectedQuotaRelease: 1,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			usageStore := &failureRecordingStore{}
			writer := store.NewAsyncUsageWriter(usageStore, 4)
			handler := MCPMiddleware(usageStore, writer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				MarkToolOutcome(r.Context(), testCase.semanticSuccess)
				_, _ = io.WriteString(w, testCase.responseBody)
			}))

			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
			requestContext := auth.WithAPIKey(request.Context(), &store.APIKey{ID: "k1"})
			requestContext = auth.WithUser(requestContext, &auth.AuthenticatedUser{User: store.User{ID: "u1"}})
			requestContext = WithToolName(requestContext, "grok_web_search")
			request = request.WithContext(requestContext)
			handler.ServeHTTP(httptest.NewRecorder(), request)
			writer.Close()

			if len(usageStore.recordedUsage) != 1 {
				t.Fatalf("recorded usage count = %d, want 1", len(usageStore.recordedUsage))
			}
			if usageStore.recordedUsage[0].Success != testCase.expectedSuccess {
				t.Fatalf("recorded success = %t, want %t", usageStore.recordedUsage[0].Success, testCase.expectedSuccess)
			}
			if usageStore.releaseSuccessCalls != testCase.expectedQuotaRelease {
				t.Fatalf("quota releases = %d, want %d", usageStore.releaseSuccessCalls, testCase.expectedQuotaRelease)
			}
		})
	}
}

func TestMCPMiddlewareCleansDebugSpoolsOnPanic(t *testing.T) {
	temporaryDirectory := t.TempDir()
	t.Setenv("TMPDIR", temporaryDirectory)
	debugStore := &debugCaptureRecordingStore{}
	handler := MCPMiddleware(debugStore, nil, debugStore)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler failed")
	}))

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("panic request body"))
	requestContext := auth.WithAPIKey(request.Context(), &store.APIKey{ID: "panic-key"})
	requestContext = WithToolName(requestContext, "grok_web_search")
	request = request.WithContext(requestContext)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected downstream panic")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}()

	entries, err := os.ReadDir(temporaryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("debug spool files remained after panic: %+v", entries)
	}
}

func (f *failureRecordingStore) ReleaseSuccessCall(_ context.Context, _ string) error {
	f.releaseSuccessCalls++
	return nil
}

func (f *failureRecordingStore) RecordUsage(_ context.Context, record store.UsageRecord) error {
	f.recordedUsage = append(f.recordedUsage, record)
	return nil
}

func TestMCPMiddlewareReleasesAndRecordsFailureOnToolErrorAndHTTPError(t *testing.T) {
	testCases := []struct {
		name    string
		handler http.Handler
	}{
		{
			name: "mcp tool isError",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"failed"}]}}`))
			}),
		},
		{
			name: "JSON-RPC top-level error",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`))
			}),
		},
		{
			name: "http failure status",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
			}),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			key := &store.APIKey{ID: "k1"}
			user := &auth.AuthenticatedUser{User: store.User{ID: "u1"}}
			st := &failureRecordingStore{}
			writer := store.NewAsyncUsageWriter(st, 8)
			h := MCPMiddleware(st, writer)(testCase.handler)

			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
			ctx := auth.WithAPIKey(req.Context(), key)
			ctx = auth.WithUser(ctx, user)
			ctx = WithToolName(ctx, "grok_web_search")
			req = req.WithContext(ctx)

			h.ServeHTTP(httptest.NewRecorder(), req)
			writer.Close()

			if st.releaseSuccessCalls != 1 {
				t.Fatalf("expected one quota release, got %d", st.releaseSuccessCalls)
			}
			if len(st.recordedUsage) != 1 {
				t.Fatalf("expected one usage record, got %+v", st.recordedUsage)
			}
			if st.recordedUsage[0].Success {
				t.Fatalf("expected unsuccessful usage record, got %+v", st.recordedUsage[0])
			}
			if st.recordedUsage[0].ToolName != "grok_web_search" {
				t.Fatalf("unexpected tool name in usage record: %+v", st.recordedUsage[0])
			}
		})
	}
}

func TestMCPMiddlewareReleasesWithLiveContextAfterRequestCancel(t *testing.T) {
	key := &store.APIKey{ID: "k1"}
	user := &auth.AuthenticatedUser{User: store.User{ID: "u1"}}
	st := &releaseContextRecordingStore{}
	h := MCPMiddleware(st, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))

	baseContext, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)).WithContext(baseContext)
	ctx := auth.WithAPIKey(req.Context(), key)
	ctx = auth.WithUser(ctx, user)
	ctx = WithToolName(ctx, "grok_web_search")
	req = req.WithContext(ctx)

	h.ServeHTTP(httptest.NewRecorder(), req)

	if st.releaseSuccessCalls != 1 {
		t.Fatalf("expected one quota release, got %d", st.releaseSuccessCalls)
	}
	if st.releaseContextErr != nil {
		t.Fatalf("quota release must detach from canceled request context, got context err %v", st.releaseContextErr)
	}
}

func TestCaptureAndRestoreRequestBodyCapsDebugCapture(t *testing.T) {
	requestBody := strings.Repeat("a", maxDebugRequestBody+37)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(requestBody))

	capturedBody, truncated := captureAndRestoreRequestBody(req)
	if !truncated {
		t.Fatal("expected debug request capture to be marked truncated")
	}
	if len(capturedBody) != maxDebugRequestBody {
		t.Fatalf("captured debug body length = %d, want %d", len(capturedBody), maxDebugRequestBody)
	}
	restoredBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredBody) != requestBody {
		t.Fatalf("request body must be restored for downstream handler, got length %d want %d", len(restoredBody), len(requestBody))
	}
}

// TestMCPMiddlewareReleasesOnPanic 验证 issue 8 的修复：当 handler panic 时，
// usage 中间件通过 defer/recover 仍会执行 ReleaseSuccessCall，
// 避免 success_calls 虚高，然后重新 panic 让上层处理。
func TestMCPMiddlewareReleasesOnPanic(t *testing.T) {
	key := &store.APIKey{ID: "k1"}
	user := &auth.AuthenticatedUser{User: store.User{ID: "u1"}}
	st := &releaseCountingStore{}
	h := MCPMiddleware(st, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"grok_web_search"}}`))
	req = req.WithContext(auth.WithAPIKey(req.Context(), key))
	req = req.WithContext(auth.WithUser(req.Context(), user))

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic to propagate")
		}
		if st.releaseSuccessCalls != 1 {
			t.Fatalf("expected release success on panic, got %d", st.releaseSuccessCalls)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), req)
}

// TestExtractToolNameMiddlewareWritesContext 验证提取中间件把工具名写入 context，
// 后续中间件无需重复解析请求体。
func TestExtractToolNameMiddlewareWritesContext(t *testing.T) {
	var gotName string
	var hasName bool
	h := ExtractToolNameMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName, hasName = ToolNameFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"grok_x_search"}}`))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !hasName {
		t.Fatal("expected tool name in context")
	}
	if gotName != "grok_x_search" {
		t.Fatalf("want grok_x_search, got %q", gotName)
	}
}

// TestMCPMiddlewareUsesContextToolName 验证当 context 已有工具名时不再重复解析 body：
// 提供一个一读就出错的 body，若中间件读取它会触发错误并跳过用量记录。
type errReader struct{}

func (errReader) Read(p []byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestMCPMiddlewareUsesContextToolName(t *testing.T) {
	key := &store.APIKey{ID: "k1"}
	user := &auth.AuthenticatedUser{User: store.User{ID: "u1"}}
	st := &fakeStore{}
	writer := store.NewAsyncUsageWriter(st, 8)
	defer writer.Close()
	h := MCPMiddleware(st, writer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", errReader{})
	req.Body = io.NopCloser(errReader{})
	ctx := auth.WithAPIKey(req.Context(), key)
	ctx = auth.WithUser(ctx, user)
	ctx = WithToolName(ctx, "grok_web_search")
	req = req.WithContext(ctx)

	h.ServeHTTP(httptest.NewRecorder(), req)
	deadline := time.Now().Add(2 * time.Second)
	for len(st.RecordedUsage()) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if recordedUsage := st.RecordedUsage(); len(recordedUsage) != 1 {
		t.Fatalf("expected one usage record via context tool name, got %d", len(recordedUsage))
	}
}
