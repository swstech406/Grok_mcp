// Package quota 的中间件测试覆盖计费准确性的关键路径：
//   - reserve 成功后 success 超限需回滚 total
//   - total 超限直接拒绝
//   - total 与 success 均超限的边界
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

	reserveTotalCalls   int
	reserveSuccessCalls int
	releaseTotalCalls   int
	releaseSuccessCalls int

	// 控制返回的错误
	reserveTotalErr   error
	reserveSuccessErr error
}

func (r *recordingStore) ReserveTotalCall(context.Context, string, int) error {
	r.reserveTotalCalls++
	return r.reserveTotalErr
}

func (r *recordingStore) ReserveSuccessCall(context.Context, string, int) error {
	r.reserveSuccessCalls++
	return r.reserveSuccessErr
}

func (r *recordingStore) ReleaseTotalCall(context.Context, string) error {
	r.releaseTotalCalls++
	return nil
}

func (r *recordingStore) ReleaseSuccessCall(context.Context, string) error {
	r.releaseSuccessCalls++
	return nil
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
	if st.reserveTotalCalls != 0 || st.reserveSuccessCalls != 0 {
		t.Fatalf("non tools/call must not reserve, got total=%d success=%d",
			st.reserveTotalCalls, st.reserveSuccessCalls)
	}
}

func TestNoUserSkipsReserve(t *testing.T) {
	st := &recordingStore{}
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := newToolCallRequest("grok_web_search")
	// 故意不带 user
	h.ServeHTTP(httptest.NewRecorder(), req)

	if st.reserveTotalCalls != 0 {
		t.Fatalf("unauthenticated request must not reserve, got total=%d", st.reserveTotalCalls)
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
	if st.reserveTotalCalls != 1 {
		t.Fatalf("want 1 reserveTotalCall, got %d", st.reserveTotalCalls)
	}
	if st.reserveSuccessCalls != 1 {
		t.Fatalf("want 1 reserveSuccessCall, got %d", st.reserveSuccessCalls)
	}
	if st.releaseTotalCalls != 0 || st.releaseSuccessCalls != 0 {
		t.Fatalf("no release on success path, got total=%d success=%d",
			st.releaseTotalCalls, st.releaseSuccessCalls)
	}
}

// TestTotalLimitExceeded 验证 total_calls 达到上限时返回 429 且不调用 ReserveSuccess。
func TestTotalLimitExceeded(t *testing.T) {
	st := &recordingStore{reserveTotalErr: store.ErrQuotaTotal}
	called := false
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := newToolCallRequest("grok_web_search")
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: "u1"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Fatal("handler must not be called when total quota exceeded")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}
	if st.reserveSuccessCalls != 0 {
		t.Fatalf("must not attempt success reserve when total fails, got %d", st.reserveSuccessCalls)
	}
	if st.releaseTotalCalls != 0 {
		t.Fatalf("must not release total when it never succeeded, got %d", st.releaseTotalCalls)
	}
}

// TestReserveTotalInternalError 验证非配错错误返回 500。
func TestReserveTotalInternalError(t *testing.T) {
	st := &recordingStore{reserveTotalErr: errors.New("db down")}
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := newToolCallRequest("grok_web_search")
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: "u1"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on store error, got %d", rec.Code)
	}
}

// TestSuccessLimitExceededRollsBackTotal 验证关键回滚路径：
// ReserveTotalCall 成功后 ReserveSuccessCall 因额度耗尽失败，必须 ReleaseTotalCall 回滚。
func TestSuccessLimitExceededRollsBackTotal(t *testing.T) {
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
	if st.reserveTotalCalls != 1 {
		t.Fatalf("want 1 reserveTotalCall, got %d", st.reserveTotalCalls)
	}
	if st.reserveSuccessCalls != 1 {
		t.Fatalf("want 1 reserveSuccessCall, got %d", st.reserveSuccessCalls)
	}
	if st.releaseTotalCalls != 1 {
		t.Fatalf("must release total after success reserve fails, got %d", st.releaseTotalCalls)
	}
	if st.releaseSuccessCalls != 0 {
		t.Fatalf("must not release success when it never succeeded, got %d", st.releaseSuccessCalls)
	}
}

// TestReserveSuccessInternalErrorRollsBackTotal 验证 success reserve 非配错错误回滚 total 并返回 500。
func TestReserveSuccessInternalErrorRollsBackTotal(t *testing.T) {
	st := &recordingStore{reserveSuccessErr: errors.New("db down")}
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := newToolCallRequest("grok_web_search")
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: "u1"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on store error, got %d", rec.Code)
	}
	if st.releaseTotalCalls != 1 {
		t.Fatalf("must release total after success reserve fails, got %d", st.releaseTotalCalls)
	}
}

// TestBothLimitsExceeded 验证 total 已超限时根本不会触及 success reserve，
// 因此也不会有回滚发生——这是 total/success 同时耗尽场景的正确行为。
func TestBothLimitsExceeded(t *testing.T) {
	st := &recordingStore{
		reserveTotalErr:   store.ErrQuotaTotal,
		reserveSuccessErr: store.ErrQuotaSuccess,
	}
	h := MCPMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := newToolCallRequest("grok_web_search")
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: "u1"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 when total exceeded, got %d", rec.Code)
	}
	if st.reserveTotalCalls != 1 {
		t.Fatalf("want 1 reserveTotalCall, got %d", st.reserveTotalCalls)
	}
	if st.reserveSuccessCalls != 0 {
		t.Fatalf("must not attempt success reserve when total fails, got %d", st.reserveSuccessCalls)
	}
	if st.releaseTotalCalls != 0 {
		t.Fatalf("no release expected when total never succeeded, got %d", st.releaseTotalCalls)
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
	if st.reserveTotalCalls != 1 || st.reserveSuccessCalls != 1 {
		t.Fatalf("fallback path must reserve both, got total=%d success=%d",
			st.reserveTotalCalls, st.reserveSuccessCalls)
	}
}
