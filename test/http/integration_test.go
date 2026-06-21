package http_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/grok-mcp/internal/admin"
	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/grok"
	mcpserver "github.com/grok-mcp/internal/mcp"
	"github.com/grok-mcp/internal/ratelimit"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/usage"
	"github.com/grok-mcp/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPAuthAndAdminFlow(t *testing.T) {
	t.Setenv("CPA_API_KEY", "cpa-key")

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "int.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	usageWriter := store.NewAsyncUsageWriter(st, 64)
	defer usageWriter.Close()

	cfg := &config.Config{
		CPABaseURL:       "http://127.0.0.1:8317",
		CPAAPIKey:        "cpa-key",
		Model:            "grok-4.3",
		DefaultRateLimit: 1000,
	}
	client := grok.NewClient(cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "grok-mcp", Version: version.Version}, nil)
	mcpserver.RegisterTools(server, client, false)

	lim := ratelimit.New(cfg.DefaultRateLimit)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	var mcpChain http.Handler = mcpHandler
	mcpChain = usage.MCPMiddleware(st, usageWriter)(mcpChain)
	mcpChain = lim.Middleware()(mcpChain)
	mcpChain = auth.APIKeyMiddleware(st)(mcpChain)

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpChain)
	adminHandler := auth.AdminTokenMiddleware("admin")(admin.NewMux(&admin.Handler{Store: st}))
	mux.Handle("/admin/", adminHandler)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Create API key via admin
	createBody := `{"name":"integration"}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/admin/v1/keys", bytes.NewBufferString(createBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create key status %d", resp.StatusCode)
	}
	var created struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	// MCP without key -> 401
	bad, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewBufferString(`{}`))
	badResp, err := http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", badResp.StatusCode)
	}

	// MCP initialize with key (may fail upstream without CPA; auth layer should pass)
	initPayload := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	good, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewBufferString(initPayload))
	good.Header.Set("Authorization", "Bearer "+created.APIKey)
	good.Header.Set("Content-Type", "application/json")
	goodResp, err := http.DefaultClient.Do(good)
	if err != nil {
		t.Fatal(err)
	}
	defer goodResp.Body.Close()
	if goodResp.StatusCode == http.StatusUnauthorized || goodResp.StatusCode == http.StatusForbidden {
		t.Fatalf("auth rejected valid key: %d", goodResp.StatusCode)
	}

	usageWriter.Close()
	time.Sleep(50 * time.Millisecond)
}