package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/app"
	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/grok"
	mcpserver "github.com/grok-mcp/internal/mcp"
	"github.com/grok-mcp/internal/panel"
	"github.com/grok-mcp/internal/ratelimit"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpAccept = "application/json, text/event-stream"

type cpaMockExpectation struct {
	APIKey   string
	Model    string
	Query    string
	ToolType string
}

type cpaResponsesRequest struct {
	Model  string              `json:"model"`
	Input  []cpaInputMessage   `json:"input"`
	Tools  []cpaToolDefinition `json:"tools"`
	Stream *bool               `json:"stream"`
}

type cpaInputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cpaToolDefinition struct {
	Type                     string          `json:"type"`
	AllowedDomains           json.RawMessage `json:"allowed_domains,omitempty"`
	ExcludedDomains          json.RawMessage `json:"excluded_domains,omitempty"`
	EnableImageUnderstanding json.RawMessage `json:"enable_image_understanding,omitempty"`
	EnableImageSearch        json.RawMessage `json:"enable_image_search,omitempty"`
}

func setMCPHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", mcpAccept)
}

func newWebSearchCPAExpectation(query string) cpaMockExpectation {
	return cpaMockExpectation{
		APIKey:   "cpa-mock-key",
		Model:    "grok-4.3",
		Query:    query,
		ToolType: "web_search",
	}
}

func newXSearchCPAExpectation(query string) cpaMockExpectation {
	return cpaMockExpectation{
		APIKey:   "cpa-mock-key",
		Model:    "grok-4.3",
		Query:    query,
		ToolType: "x_search",
	}
}

func cpaMockSSE(t *testing.T, expectations ...cpaMockExpectation) *httptest.Server {
	t.Helper()
	if len(expectations) > 1 {
		t.Fatalf("cpaMockSSE accepts at most one expectation, got %d", len(expectations))
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(expectations) == 0 {
			t.Errorf("CPA mock received unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected CPA mock request", http.StatusInternalServerError)
			return
		}

		expected := expectations[0]
		if err := validateCPAMockRequest(r, expected); err != nil {
			t.Errorf("CPA mock received invalid request: %v", err)
			http.Error(w, "invalid CPA mock request", http.StatusBadRequest)
			return
		}
		writeCPAMockSSEResponse(w, "mock integration answer")
	}))
}

func writeCPAMockSSEResponse(w http.ResponseWriter, answer string) {
	responseJSON := fmt.Sprintf(`{"output":[{"role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, answer)
	completed := `{"type":"response.completed","response":` + strings.TrimSpace(responseJSON) + `}`
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: " + completed + "\n\n"))
}

func validateCPAMockRequest(r *http.Request, expected cpaMockExpectation) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("method = %q, want %q", r.Method, http.MethodPost)
	}
	if r.URL.Path != "/v1/responses" {
		return fmt.Errorf("path = %q, want %q", r.URL.Path, "/v1/responses")
	}
	if r.URL.RawQuery != "" {
		return fmt.Errorf("raw query = %q, want empty", r.URL.RawQuery)
	}
	if got, want := r.Header.Get("Authorization"), "Bearer "+expected.APIKey; got != want {
		return fmt.Errorf("Authorization header = %q, want %q", got, want)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		return fmt.Errorf("Content-Type header = %q, want %q", got, "application/json")
	}
	if got := r.Header.Get("Accept"); got != "text/event-stream" {
		return fmt.Errorf("Accept header = %q, want %q", got, "text/event-stream")
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	var req cpaResponsesRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return fmt.Errorf("decode request body %q: %w", truncate(string(body), 512), err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("request body contains trailing JSON data: %w", err)
	}

	if req.Model != expected.Model {
		return fmt.Errorf("model = %q, want %q", req.Model, expected.Model)
	}
	if req.Stream == nil || !*req.Stream {
		return fmt.Errorf("stream = %v, want true", req.Stream)
	}
	if len(req.Input) != 1 {
		return fmt.Errorf("input length = %d, want 1", len(req.Input))
	}
	if req.Input[0].Role != "user" {
		return fmt.Errorf("input[0].role = %q, want %q", req.Input[0].Role, "user")
	}
	if req.Input[0].Content != expected.Query {
		return fmt.Errorf("input[0].content = %q, want %q", req.Input[0].Content, expected.Query)
	}
	if len(req.Tools) != 1 {
		return fmt.Errorf("tools length = %d, want 1", len(req.Tools))
	}
	if req.Tools[0].Type != expected.ToolType {
		return fmt.Errorf("tools[0].type = %q, want %q", req.Tools[0].Type, expected.ToolType)
	}
	if len(req.Tools[0].AllowedDomains) != 0 {
		return fmt.Errorf("tools[0].allowed_domains unexpectedly present: %s", req.Tools[0].AllowedDomains)
	}
	if len(req.Tools[0].ExcludedDomains) != 0 {
		return fmt.Errorf("tools[0].excluded_domains unexpectedly present: %s", req.Tools[0].ExcludedDomains)
	}
	if len(req.Tools[0].EnableImageUnderstanding) != 0 {
		return fmt.Errorf("tools[0].enable_image_understanding unexpectedly present: %s", req.Tools[0].EnableImageUnderstanding)
	}
	if len(req.Tools[0].EnableImageSearch) != 0 {
		return fmt.Errorf("tools[0].enable_image_search unexpectedly present: %s", req.Tools[0].EnableImageSearch)
	}

	return nil
}

func TestHTTPPanelAndMCPFlow(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "int.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cpa := cpaMockSSE(t, newWebSearchCPAExpectation("integration test"))
	defer cpa.Close()

	usageWriter := store.NewAsyncUsageWriter(st, 64)

	cfg := &config.Config{
		CPABaseURL: cpa.URL,
		CPAAPIKey:  "cpa-mock-key",
		Model:      "grok-4.3",
		JWTSecret:  "jwt-secret-must-be-at-least-32-bytes!",
		Timeout:    30 * time.Second,
	}
	client := grok.NewClient(cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "grok-mcp", Version: version.Version}, nil)
	mcpserver.RegisterTools(server, client, false)

	userLimiter := ratelimit.NewUserLimiter()
	defer userLimiter.Close()
	mcpIPLimiter := ratelimit.NewIPLimiter(10000)
	defer mcpIPLimiter.Close()

	authResolver := auth.NewCachedAPIKeyResolver(st, 30*time.Second)
	panelHandler := &panel.Handler{
		Store:                 st,
		JWTSecret:             cfg.JWTSecret,
		InitialServerSettings: cfg.ServerSettings(),
		AuthCache:             authResolver,
	}
	handler := app.BuildHTTPHandler(app.HTTPDependencies{
		Store:          st,
		MCPServer:      server,
		UsageWriter:    usageWriter,
		UserLimiter:    userLimiter,
		MCPIPLimiter:   mcpIPLimiter,
		APIKeyResolver: authResolver,
		PanelHandler:   panelHandler,
	})

	ts := httptest.NewServer(handler)
	defer ts.Close()

	regBody := `{"username":"intuser","password":"password123"}`
	regReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(regBody))
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	regResp.Body.Close()
	if regResp.StatusCode != http.StatusCreated {
		t.Fatalf("register %d", regResp.StatusCode)
	}

	loginBody := `{"username":"intuser","password":"password123"}`
	loginReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/login", bytes.NewBufferString(loginBody))
	loginResp, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	var login panel.LoginResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}

	keyBody := `{"name":"integration"}`
	keyReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/keys", bytes.NewBufferString(keyBody))
	keyReq.Header.Set("Authorization", "Bearer "+login.Token)
	keyResp, err := http.DefaultClient.Do(keyReq)
	if err != nil {
		t.Fatal(err)
	}
	defer keyResp.Body.Close()
	var created panel.CreateKeyResponse
	if err := json.NewDecoder(keyResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if keyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create key status %d", keyResp.StatusCode)
	}
	keyID := created.Key.ID

	bad, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewBufferString(`{}`))
	badResp, err := http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", badResp.StatusCode)
	}

	initPayload := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	initReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewBufferString(initPayload))
	setMCPHeaders(initReq, created.APIKey)
	initResp, err := http.DefaultClient.Do(initReq)
	if err != nil {
		t.Fatal(err)
	}
	initBody, _ := io.ReadAll(initResp.Body)
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status %d body=%s", initResp.StatusCode, truncate(string(initBody), 512))
	}

	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"integration test"}}}`
	toolReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewBufferString(toolPayload))
	setMCPHeaders(toolReq, created.APIKey)
	toolResp, err := http.DefaultClient.Do(toolReq)
	if err != nil {
		t.Fatal(err)
	}
	toolBody, _ := io.ReadAll(toolResp.Body)
	toolResp.Body.Close()
	if toolResp.StatusCode != http.StatusOK {
		t.Fatalf("tools/call status %d body=%s", toolResp.StatusCode, truncate(string(toolBody), 1024))
	}
	if !strings.Contains(string(toolBody), "mock integration answer") {
		t.Fatalf("tools/call response missing mock answer: %s", truncate(string(toolBody), 1024))
	}

	usageWriter.Close()

	since := time.Now().UTC().Add(-time.Hour)
	stats, err := st.GetUsageStats(context.Background(), keyID, since)
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if stats.TotalCalls != 1 {
		t.Fatalf("expected 1 usage_log row for tools/call, got total=%d stats=%+v", stats.TotalCalls, stats)
	}
	if stats.ByTool["grok_web_search"] != 1 {
		t.Fatalf("expected grok_web_search in usage stats, got %+v", stats.ByTool)
	}
	if stats.SuccessCalls != 1 {
		t.Fatalf("expected successful tools/call recorded, success=%d", stats.SuccessCalls)
	}

	k, err := st.GetKeyByID(context.Background(), keyID)
	if err != nil {
		t.Fatal(err)
	}
	if k.TotalCalls < 1 {
		t.Fatalf("expected key total_calls incremented, got %d", k.TotalCalls)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
