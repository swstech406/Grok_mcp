package usage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
)

// fakeStore 仅记录 TouchKeyUsage 调用，其余方法返回零值，用于断言用量计费的门控逻辑。
type fakeStore struct {
	touched int
	lastID  string
}

func (f *fakeStore) Close() error { return nil }
func (f *fakeStore) CreateKey(context.Context, string, int) (*store.APIKey, string, error) {
	return nil, "", nil
}
func (f *fakeStore) GetKeyByHash(context.Context, string) (*store.APIKey, error) { return nil, nil }
func (f *fakeStore) ListKeys(context.Context) ([]*store.APIKey, error)           { return nil, nil }
func (f *fakeStore) GetKeyByID(context.Context, string) (*store.APIKey, error) {
	return nil, fmt.Errorf("not found")
}
func (f *fakeStore) UpdateKey(context.Context, string, store.KeyUpdates) (*store.APIKey, error) {
	return nil, nil
}
func (f *fakeStore) DeleteKey(context.Context, string) error              { return nil }
func (f *fakeStore) RecordUsage(context.Context, store.UsageRecord) error { return nil }
func (f *fakeStore) GetUsageStats(context.Context, string, time.Time) (*store.UsageStats, error) {
	return nil, nil
}
func (f *fakeStore) GetGlobalStats(context.Context, time.Time) (*store.UsageStats, error) {
	return nil, nil
}
func (f *fakeStore) TouchKeyUsage(_ context.Context, keyID string) error {
	f.touched++
	f.lastID = keyID
	return nil
}

func TestMCPMiddlewareGatesUsageByToolCall(t *testing.T) {
	key := &store.APIKey{ID: "k1"}
	st := &fakeStore{}
	h := MCPMiddleware(st, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	// 非 tools/call（如 initialize）不计入用量。
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"initialize"}`))
	req = req.WithContext(auth.WithAPIKey(req.Context(), key))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if st.touched != 0 {
		t.Fatalf("initialize must not touch usage, got touched=%d", st.touched)
	}

	// tools/call 计入用量一次。
	req2 := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"grok_web_search"}}`))
	req2 = req2.WithContext(auth.WithAPIKey(req2.Context(), key))
	h.ServeHTTP(httptest.NewRecorder(), req2)
	if st.touched != 1 || st.lastID != "k1" {
		t.Fatalf("tools/call should touch usage once for k1, got touched=%d id=%q", st.touched, st.lastID)
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
