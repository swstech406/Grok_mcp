// Package quota 的中间件测试覆盖成功请求额度预留的关键路径：
//   - success 预留成功后放行 handler
//   - success 超限直接拒绝
//   - success 存储错误返回 500
//   - 非 tools/call 请求不触发预留
//   - 未鉴权用户不触发预留
package quota

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/usage"
)

// recordingStore 记录 Reserve/Release 调用顺序与次数，用于断言回滚逻辑。
type recordingStore struct {
	store.TestStore

	reserveSuccessCalls int
	lastUserID          string
	lastSuccessLimit    int

	// 控制返回的错误
	reserveSuccessErr error
}

func (r *recordingStore) ReserveSuccessCall(_ context.Context, userID string, successLimit int) error {
	r.reserveSuccessCalls++
	r.lastUserID = userID
	r.lastSuccessLimit = successLimit
	return r.reserveSuccessErr
}

// newToolCallRequest 构造一个 tools/call 请求并预先把工具名写入 context，
// 模拟链路中 ExtractToolNameMiddleware 已运行的情况。
func newToolCallRequest(name string) *http.Request {
	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"` + name + `"}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r = r.WithContext(usage.WithToolName(r.Context(), name))
	return r
}

// newNoToolCallRequest 构造 initialize 请求；ExtractToolNameMiddleware 会写入空名。
func newNoToolCallRequest() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"initialize"}`))
	r = r.WithContext(usage.WithToolName(r.Context(), ""))
	return r
}

func TestNonToolsCallSkipsReserve(t *testing.T) {
	st := &recordingStore{}
	called := false
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	h.ServeHTTP(httptest.NewRecorder(), newNoToolCallRequest())

	if !called {
		t.Fatal("non tools/call should pass through to handler")
	}
	if st.reserveSuccessCalls != 0 {
		t.Fatalf("non tools/call must not reserve success calls, got %d", st.reserveSuccessCalls)
	}
}

func TestNoUserSkipsReserve(t *testing.T) {
	st := &recordingStore{}
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := newToolCallRequest("grok_web_search")
	// 故意不带 user
	h.ServeHTTP(httptest.NewRecorder(), req)

	if st.reserveSuccessCalls != 0 {
		t.Fatalf("unauthenticated request must not reserve success calls, got %d", st.reserveSuccessCalls)
	}
}

func TestReserveSuccessAndForward(t *testing.T) {
	st := &recordingStore{}
	called := false
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := newToolCallRequest("grok_web_search")
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: "u1"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler should be called when quota reserve succeeds")
	}
	if st.reserveSuccessCalls != 1 {
		t.Fatalf("want 1 reserveSuccessCall, got %d", st.reserveSuccessCalls)
	}
}

func TestReserveUsesAuthenticatedUserLimit(t *testing.T) {
	st := &recordingStore{}
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	user := &store.User{ID: "paid-user", SuccessLimit: 123}
	req := newToolCallRequest("grok_x_search")
	req = req.WithContext(auth.WithUser(req.Context(), user))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if st.lastUserID != user.ID || st.lastSuccessLimit != user.SuccessLimit {
		t.Fatalf("reserve got user=%q limit=%d, want user=%q limit=%d", st.lastUserID, st.lastSuccessLimit, user.ID, user.SuccessLimit)
	}
}

// TestSuccessLimitExceeded 验证 success_calls 达到上限时返回 429 且不调用 handler。
func TestSuccessLimitExceeded(t *testing.T) {
	st := &recordingStore{reserveSuccessErr: store.ErrQuotaSuccess}
	called := false
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := newToolCallRequest("grok_web_search")
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: "u1"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Fatal("handler must not be called when success quota exceeded")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}
	if st.reserveSuccessCalls != 1 {
		t.Fatalf("want 1 reserveSuccessCall, got %d", st.reserveSuccessCalls)
	}
}

// TestReserveSuccessInternalError 验证 success reserve 非额度错误返回 500。
func TestReserveSuccessInternalError(t *testing.T) {
	st := &recordingStore{reserveSuccessErr: errors.New("db down")}
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := newToolCallRequest("grok_web_search")
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: "u1"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on store error, got %d", rec.Code)
	}
}

// TestFallbackToPeekWhenNoContextName 验证未挂载 ExtractToolNameMiddleware 的旧链路：
// quota.MCPMiddleware 会回退到 usage.PeekToolName 解析一次。
func TestFallbackToPeekWhenNoContextName(t *testing.T) {
	st := &recordingStore{}
	called := false
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 下游应能读取完整 body
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "grok_web_search") {
			t.Fatalf("body not restored downstream: %q", b)
		}
		called = true
	}))

	// 不写入 context 工具名，模拟旧链路
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"grok_web_search"}}`))
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: "u1"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler should be called via fallback peek path")
	}
	if st.reserveSuccessCalls != 1 {
		t.Fatalf("fallback path must reserve success once, got %d", st.reserveSuccessCalls)
	}
}
