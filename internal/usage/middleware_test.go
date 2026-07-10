package usage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
)

// fakeStore 的 touched/lastID 字段会被 AsyncUsageWriter 后台 goroutine 写入、
// 测试主 goroutine 读取，因此用 mutex 保护以避免数据竞争。
type fakeStore struct {
	store.TestStore
	mu      sync.Mutex
	touched int
	lastID  string
}

func (f *fakeStore) TouchKeyUsage(_ context.Context, keyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched++
	f.lastID = keyID
	return nil
}

func (f *fakeStore) ReleaseSuccessCall(context.Context, string) error { return nil }

func (f *fakeStore) TryIncrementUserSuccessCalls(context.Context, string, int) error {
	return nil
}

func (f *fakeStore) Touched() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.touched
}

func (f *fakeStore) LastID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastID
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
	if st.Touched() != 0 {
		t.Fatalf("initialize must not touch usage, got touched=%d", st.Touched())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"grok_web_search"}}`))
	req2 = req2.WithContext(auth.WithAPIKey(req2.Context(), key))
	req2 = req2.WithContext(auth.WithUser(req2.Context(), user))
	h.ServeHTTP(httptest.NewRecorder(), req2)
	deadline := time.Now().Add(2 * time.Second)
	for st.Touched() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st.Touched() != 1 || st.LastID() != "k1" {
		t.Fatalf("tools/call should touch usage once for k1, got touched=%d id=%q", st.Touched(), st.LastID())
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
	touchedKeys         []string
	recordedUsage       []store.UsageRecord
}

func (f *failureRecordingStore) TouchKeyUsage(_ context.Context, keyID string) error {
	f.touchedKeys = append(f.touchedKeys, keyID)
	return nil
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
			if len(st.touchedKeys) != 1 || st.touchedKeys[0] != "k1" {
				t.Fatalf("expected one touch for k1, got %+v", st.touchedKeys)
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
	for st.Touched() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st.Touched() != 1 {
		t.Fatalf("expected usage touched via context tool name, got %d", st.Touched())
	}
}
