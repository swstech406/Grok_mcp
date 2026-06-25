package http_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/grok"
	mcpserver "github.com/grok-mcp/internal/mcp"
	"github.com/grok-mcp/internal/panel"
	"github.com/grok-mcp/internal/quota"
	"github.com/grok-mcp/internal/ratelimit"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/usage"
	"github.com/grok-mcp/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPPanelAndMCPFlow(t *testing.T) {
	t.Setenv("CPA_API_KEY", "cpa-key")

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "int.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	usageWriter := store.NewAsyncUsageWriter(st, 64)
	defer usageWriter.Close()

	cfg := &config.Config{
		CPABaseURL:              "http://127.0.0.1:8317",
		CPAAPIKey:               "cpa-key",
		Model:                   "grok-4.3",
		JWTSecret:               "jwt-secret",
		DefaultUserRPM:          1000,
		DefaultUserTotalLimit:   0,
		DefaultUserSuccessLimit: 0,
	}
	client := grok.NewClient(cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "grok-mcp", Version: version.Version}, nil)
	mcpserver.RegisterTools(server, client, false)

	userLim := ratelimit.NewUserLimiter(cfg.DefaultUserRPM)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	var mcpChain http.Handler = mcpHandler
	mcpChain = usage.MCPMiddleware(st, usageWriter)(mcpChain)
	mcpChain = quota.MCPMiddleware(st)(mcpChain)
	mcpChain = usage.ExtractToolNameMiddleware()(mcpChain)
	mcpChain = userLim.UserMiddleware()(mcpChain)
	mcpChain = auth.APIKeyMiddleware(st)(mcpChain)

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpChain)

	ph := &panel.Handler{Store: st, Config: cfg}
	pm := panel.NewMux(ph)
	skip := map[string]struct{}{
		"/panel/v1/auth/register": {},
		"/panel/v1/auth/login":    {},
	}
	var panelChain http.Handler = pm
	panelChain = auth.JWTMiddleware(cfg.JWTSecret, st, skip)(panelChain)
	mux.Handle("/panel/", panelChain)

	ts := httptest.NewServer(mux)
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
	var login panel.LoginResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()

	keyBody := `{"name":"integration"}`
	keyReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/keys", bytes.NewBufferString(keyBody))
	keyReq.Header.Set("Authorization", "Bearer "+login.Token)
	keyResp, err := http.DefaultClient.Do(keyReq)
	if err != nil {
		t.Fatal(err)
	}
	var created panel.CreateKeyResponse
	if err := json.NewDecoder(keyResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	keyResp.Body.Close()
	if keyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create key status %d", keyResp.StatusCode)
	}

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