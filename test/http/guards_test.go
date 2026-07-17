package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/app"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/logx"
	mcpserver "github.com/MapleMapleCat/Grok_Search_Mcp/internal/mcp"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/panel"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type integrationEnv struct {
	ts      *httptest.Server
	st      *store.SQLiteStore
	writer  *store.AsyncUsageWriter
	userLim *ratelimit.UserLimiter
	created panel.CreateKeyResponse
	login   panel.LoginResponse
}

func bootIntegrationEnv(t *testing.T, cpa *httptest.Server) *integrationEnv {
	return bootIntegrationEnvWithSearchConcurrency(t, cpa, 16, 4)
}

func bootIntegrationEnvWithSearchConcurrency(t *testing.T, cpa *httptest.Server, globalLimit, perUserLimit int) *integrationEnv {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "guards.db"))
	if err != nil {
		t.Fatal(err)
	}
	writer := store.NewAsyncUsageWriter(st, 64)
	cfg := &config.Config{
		CPABaseURL:       cpa.URL,
		CPAAPIKey:        "cpa-mock-key",
		UpstreamProtocol: config.UpstreamProtocolResponses,
		Model:            "grok-4.3",
		JWTSecret:        "jwt-secret-must-be-at-least-32-bytes!",
		Timeout:          30 * time.Second,
		RegistrationMode: store.RegistrationModeFree,
	}
	if err := st.ConfigureAPIKeyEncryption(cfg.JWTSecret); err != nil {
		t.Fatal(err)
	}
	client, err := grok.NewClientWithServerSettings(cfg.ServerSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "grok-mcp", Version: version.Version}, nil)
	mcpserver.RegisterToolsWithLogger(server, client, logx.New("mcp-test", false))
	userLimiter := ratelimit.NewUserLimiter()
	searchConcurrencyLimiter := ratelimit.NewSearchConcurrencyLimiter(globalLimit, perUserLimit)
	mcpIPLimiter := ratelimit.NewIPLimiter(10000)
	authResolver := auth.NewCachedAPIKeyResolver(st, 30*time.Second)
	panelHandler := &panel.Handler{
		Store:                 st,
		JWTSecret:             cfg.JWTSecret,
		InitialServerSettings: cfg.ServerSettings(),
		AuthCache:             authResolver,
	}
	handler := app.BuildHTTPHandler(app.HTTPDependencies{
		Store:                    st,
		MCPServer:                server,
		UsageWriter:              writer,
		UserLimiter:              userLimiter,
		SearchConcurrencyLimiter: searchConcurrencyLimiter,
		MCPIPLimiter:             mcpIPLimiter,
		APIKeyResolver:           authResolver,
		PanelHandler:             panelHandler,
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(func() {
		ts.Close()
		authResolver.Close()
		userLimiter.Close()
		searchConcurrencyLimiter.Close()
		mcpIPLimiter.Close()
		writer.Close()
		st.Close()
	})

	regReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(`{"username":"guarduser","password":"password123"}`))
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	regResp.Body.Close()

	loginReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/login", bytes.NewBufferString(`{"username":"guarduser","password":"password123"}`))
	loginResp, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	var login panel.LoginResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}

	keyReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/keys", bytes.NewBufferString(`{"name":"guard-key"}`))
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
		t.Fatalf("create key %d", keyResp.StatusCode)
	}
	return &integrationEnv{ts: ts, st: st, writer: writer, userLim: userLimiter, created: created, login: login}
}

func TestHTTPSearchConcurrencyRejectsBeforeUpstreamAndUsage(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstreamRequestStarted := make(chan struct{})
	releaseUpstreamRequest := make(chan struct{})
	var releaseUpstreamOnce sync.Once
	releaseUpstream := func() {
		releaseUpstreamOnce.Do(func() { close(releaseUpstreamRequest) })
	}
	defer releaseUpstream()

	cpa := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if err := validateCPAMockRequest(request, newWebSearchCPAExpectation("held search")); err != nil {
			t.Errorf("CPA mock received invalid request: %v", err)
			http.Error(responseWriter, "invalid CPA mock request", http.StatusBadRequest)
			return
		}
		upstreamCalls.Add(1)
		close(upstreamRequestStarted)
		<-releaseUpstreamRequest
		writeCPAMockSSEResponse(responseWriter, "held search answer")
	}))
	defer cpa.Close()

	environment := bootIntegrationEnvWithSearchConcurrency(t, cpa, 2, 1)
	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"held search"}}}`

	type firstRequestResult struct {
		statusCode int
		body       string
		err        error
	}
	firstRequestDone := make(chan firstRequestResult, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, environment.ts.URL+"/mcp", bytes.NewBufferString(toolPayload))
		setMCPHeaders(request, environment.created.APIKey)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			firstRequestDone <- firstRequestResult{err: err}
			return
		}
		responseBody, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		firstRequestDone <- firstRequestResult{
			statusCode: response.StatusCode,
			body:       string(responseBody),
			err:        readErr,
		}
	}()

	select {
	case <-upstreamRequestStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the first upstream search")
	}

	secondRequest, _ := http.NewRequest(http.MethodPost, environment.ts.URL+"/mcp", bytes.NewBufferString(toolPayload))
	setMCPHeaders(secondRequest, environment.created.APIKey)
	secondResponse, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondResponseBody, err := io.ReadAll(secondResponse.Body)
	secondResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	releaseUpstream()
	firstResult := <-firstRequestDone
	if firstResult.err != nil {
		t.Fatalf("first search request failed: %v", firstResult.err)
	}
	if firstResult.statusCode != http.StatusOK || !strings.Contains(firstResult.body, "held search answer") {
		t.Fatalf("first search status=%d body=%s", firstResult.statusCode, truncate(firstResult.body, 512))
	}
	if secondResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second search status=%d, want %d", secondResponse.StatusCode, http.StatusServiceUnavailable)
	}
	if secondResponse.Header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After=%q, want 1", secondResponse.Header.Get("Retry-After"))
	}
	if secondResponse.Header.Get(ratelimit.SearchQueueTimeHeader) == "" {
		t.Fatalf("missing %s header", ratelimit.SearchQueueTimeHeader)
	}
	if !strings.Contains(string(secondResponseBody), "user search concurrency limit reached") {
		t.Fatalf("unexpected concurrency rejection body: %s", truncate(string(secondResponseBody), 512))
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", upstreamCalls.Load())
	}

	environment.writer.Close()
	usageStats, err := environment.st.GetUsageStats(
		context.Background(),
		environment.created.Key.ID,
		time.Now().UTC().Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if usageStats.TotalCalls != 1 {
		t.Fatalf("usage calls=%d, want only the admitted search", usageStats.TotalCalls)
	}
}

func TestHTTPPanelKeysRequireJWT(t *testing.T) {
	cpa := cpaMockSSE(t)
	defer cpa.Close()
	env := bootIntegrationEnv(t, cpa)

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/panel/v1/keys", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without JWT, got %d", resp.StatusCode)
	}
}

func TestHTTPPanelRegistrationSettingsDoesNotRequireJWT(t *testing.T) {
	cpa := cpaMockSSE(t)
	defer cpa.Close()
	env := bootIntegrationEnv(t, cpa)

	request, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/panel/v1/auth/registration-settings", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected public registration settings to return 200, got %d", response.StatusCode)
	}
}

func TestHTTPPanelAdminRoutesRequireAdminRole(t *testing.T) {
	cpa := cpaMockSSE(t)
	defer cpa.Close()
	env := bootIntegrationEnv(t, cpa)

	request, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/panel/v1/admin/users", nil)
	request.Header.Set("Authorization", "Bearer "+env.login.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected regular user to receive 403 for admin route, got %d", response.StatusCode)
	}
}

func TestHTTPMCPDisabledAPIKeyForbidden(t *testing.T) {
	cpa := cpaMockSSE(t)
	defer cpa.Close()
	env := bootIntegrationEnv(t, cpa)
	ctx := context.Background()
	dis := false
	if _, err := env.st.UpdateKey(ctx, env.created.Key.ID, store.KeyUpdates{Enabled: &dis}); err != nil {
		t.Fatal(err)
	}

	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"x"}}}`
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/mcp", bytes.NewBufferString(toolPayload))
	setMCPHeaders(req, env.created.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 disabled key, got %d body=%s", resp.StatusCode, truncate(string(body), 256))
	}
}

func TestHTTPPanelKeyUpdateInvalidatesMCPAuthCache(t *testing.T) {
	var upstreamCalls atomic.Int64
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateCPAMockRequest(r, newWebSearchCPAExpectation("warm cache")); err != nil {
			t.Errorf("CPA mock received invalid request: %v", err)
			http.Error(w, "invalid CPA mock request", http.StatusBadRequest)
			return
		}
		upstreamCalls.Add(1)
		writeCPAMockSSEResponse(w, "cache warm answer")
	}))
	defer cpa.Close()

	env := bootIntegrationEnv(t, cpa)
	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"warm cache"}}}`
	firstStatus, firstBody := callMCPTool(t, env, toolPayload)
	if firstStatus != http.StatusOK || !strings.Contains(firstBody, "cache warm answer") {
		t.Fatalf("expected first call to warm auth cache, status=%d body=%s", firstStatus, truncate(firstBody, 512))
	}

	disableRequest, _ := http.NewRequest(http.MethodPatch, env.ts.URL+"/panel/v1/keys/"+env.created.Key.ID, bytes.NewBufferString(`{"enabled":false}`))
	disableRequest.Header.Set("Authorization", "Bearer "+env.login.Token)
	disableResponse, err := http.DefaultClient.Do(disableRequest)
	if err != nil {
		t.Fatal(err)
	}
	disableBody, _ := io.ReadAll(disableResponse.Body)
	disableResponse.Body.Close()
	if disableResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected key disable through panel to return 200, got %d body=%s", disableResponse.StatusCode, truncate(string(disableBody), 512))
	}

	secondStatus, secondBody := callMCPTool(t, env, toolPayload)
	if secondStatus != http.StatusForbidden {
		t.Fatalf("expected disabled cached key to be rejected after invalidation, status=%d body=%s", secondStatus, truncate(secondBody, 512))
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("expected rejected cached key to skip upstream, upstream calls=%d", upstreamCalls.Load())
	}
}

func TestHTTPMCPDisabledUserForbidden(t *testing.T) {
	cpa := cpaMockSSE(t)
	defer cpa.Close()
	env := bootIntegrationEnv(t, cpa)
	ctx := context.Background()
	key, err := env.st.GetKeyByID(ctx, env.created.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	dis := false
	if _, err := env.st.UpdateUser(ctx, key.UserID, store.UserUpdates{Enabled: &dis}); err != nil {
		t.Fatal(err)
	}

	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"x"}}}`
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/mcp", bytes.NewBufferString(toolPayload))
	setMCPHeaders(req, env.created.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 disabled user, got %d body=%s", resp.StatusCode, truncate(string(body), 256))
	}
}

func TestHTTPToolCallUpstreamFailureRecordsUnsuccessfulUsage(t *testing.T) {
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateCPAMockRequest(r, newWebSearchCPAExpectation("fail upstream")); err != nil {
			t.Errorf("CPA mock received invalid request: %v", err)
			http.Error(w, "invalid CPA mock request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad"))
	}))
	defer cpa.Close()
	env := bootIntegrationEnv(t, cpa)
	keyID := env.created.Key.ID

	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"fail upstream"}}}`
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/mcp", bytes.NewBufferString(toolPayload))
	setMCPHeaders(req, env.created.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/call HTTP %d body=%s", resp.StatusCode, truncate(string(body), 512))
	}
	if !strings.Contains(string(body), `"isError":true`) {
		t.Fatalf("expected MCP isError tool result, got %s", truncate(string(body), 512))
	}

	env.writer.Close()
	since := time.Now().UTC().Add(-time.Hour)
	stats, err := env.st.GetUsageStats(context.Background(), keyID, since)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalCalls != 1 {
		t.Fatalf("expected usage row, got %+v", stats)
	}
	if stats.SuccessCalls != 0 {
		t.Fatalf("expected unsuccessful usage, success=%d", stats.SuccessCalls)
	}
}

func TestHTTPJSONRPCErrorsReleaseSuccessQuota(t *testing.T) {
	testCases := []struct {
		name           string
		invalidPayload string
	}{
		{
			name:           "unknown tool",
			invalidPayload: `{"jsonrpc":"2.0","id":101,"method":"tools/call","params":{"name":"grok_unknown_tool","arguments":{"query":"must not reach upstream"}}}`,
		},
		{
			name:           "invalid tool parameters",
			invalidPayload: `{"jsonrpc":"2.0","id":102,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":123}}}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var upstreamCalls atomic.Int64
			cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := validateCPAMockRequest(r, newWebSearchCPAExpectation("quota release check")); err != nil {
					t.Errorf("CPA mock received invalid request: %v", err)
					http.Error(w, "invalid CPA mock request", http.StatusBadRequest)
					return
				}
				upstreamCalls.Add(1)
				writeCPAMockSSEResponse(w, "quota released answer")
			}))
			defer cpa.Close()

			env := bootIntegrationEnv(t, cpa)
			requestContext := context.Background()
			tier0, err := env.st.GetTierByName(requestContext, "tier0")
			if err != nil || tier0 == nil {
				t.Fatalf("tier0 should be seeded by migration: %v", err)
			}
			singleSuccessLimit := 1
			if _, err := env.st.UpdateTier(requestContext, tier0.ID, store.TierUpdates{SuccessLimit: &singleSuccessLimit}); err != nil {
				t.Fatal(err)
			}

			invalidStatus, invalidBody := callMCPTool(t, env, testCase.invalidPayload)
			if invalidStatus != http.StatusOK {
				t.Fatalf("expected JSON-RPC failure over HTTP 200, status=%d body=%s", invalidStatus, truncate(invalidBody, 512))
			}
			requireJSONRPCTopLevelError(t, invalidBody)
			if upstreamCalls.Load() != 0 {
				t.Fatalf("invalid tools/call must not reach upstream; upstream calls=%d", upstreamCalls.Load())
			}

			validPayload := `{"jsonrpc":"2.0","id":103,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"quota release check"}}}`
			validStatus, validBody := callMCPTool(t, env, validPayload)
			if validStatus != http.StatusOK || !strings.Contains(validBody, "quota released answer") {
				t.Fatalf("expected valid call after JSON-RPC failure to use released quota, status=%d body=%s", validStatus, truncate(validBody, 512))
			}
			if upstreamCalls.Load() != 1 {
				t.Fatalf("expected only the valid call to reach upstream; upstream calls=%d", upstreamCalls.Load())
			}

			env.writer.Close()
			stats, err := env.st.GetUsageStats(requestContext, env.created.Key.ID, time.Now().UTC().Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if stats.TotalCalls != 2 || stats.SuccessCalls != 1 {
				t.Fatalf("expected one failed and one successful usage record, got %+v", stats)
			}
		})
	}
}

func TestHTTPMCPQuotaExhaustionSkipsUpstreamAndUsage(t *testing.T) {
	var upstreamCalls atomic.Int64
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateCPAMockRequest(r, newWebSearchCPAExpectation("quota test")); err != nil {
			t.Errorf("CPA mock received invalid request: %v", err)
			http.Error(w, "invalid CPA mock request", http.StatusBadRequest)
			return
		}
		upstreamCalls.Add(1)
		writeCPAMockSSEResponse(w, "quota success answer")
	}))
	defer cpa.Close()

	env := bootIntegrationEnv(t, cpa)
	ctx := context.Background()
	tier0, err := env.st.GetTierByName(ctx, "tier0")
	if err != nil || tier0 == nil {
		t.Fatalf("tier0 should be seeded by migration: %v", err)
	}
	singleSuccessLimit := 1
	if _, err := env.st.UpdateTier(ctx, tier0.ID, store.TierUpdates{SuccessLimit: &singleSuccessLimit}); err != nil {
		t.Fatal(err)
	}

	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"quota test"}}}`
	firstStatus, firstBody := callMCPTool(t, env, toolPayload)
	if firstStatus != http.StatusOK || !strings.Contains(firstBody, "quota success answer") {
		t.Fatalf("expected first tools/call success, status=%d body=%s", firstStatus, truncate(firstBody, 512))
	}

	secondStatus, secondBody := callMCPTool(t, env, toolPayload)
	if secondStatus != http.StatusTooManyRequests {
		t.Fatalf("expected quota exhaustion 429, got %d body=%s", secondStatus, truncate(secondBody, 512))
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("expected exhausted quota to skip upstream; upstream calls=%d", upstreamCalls.Load())
	}

	env.writer.Close()
	stats, err := env.st.GetUsageStats(ctx, env.created.Key.ID, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalCalls != 1 || stats.SuccessCalls != 1 {
		t.Fatalf("expected only successful admitted call to be recorded, got %+v", stats)
	}
}

func TestHTTPMCPXSearchFlowForwardsXTool(t *testing.T) {
	var upstreamCalls atomic.Int64
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateCPAMockRequest(r, newXSearchCPAExpectation("x integration")); err != nil {
			t.Errorf("CPA mock received invalid request: %v", err)
			http.Error(w, "invalid CPA mock request", http.StatusBadRequest)
			return
		}
		upstreamCalls.Add(1)
		writeCPAMockSSEResponse(w, "mock x answer")
	}))
	defer cpa.Close()

	env := bootIntegrationEnv(t, cpa)
	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_x_search","arguments":{"query":"x integration"}}}`
	status, body := callMCPTool(t, env, toolPayload)
	if status != http.StatusOK || !strings.Contains(body, "mock x answer") {
		t.Fatalf("expected x_search tools/call success, status=%d body=%s", status, truncate(body, 512))
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("expected one x_search upstream call, got %d", upstreamCalls.Load())
	}
}

func callMCPTool(t *testing.T, env *integrationEnv, payload string) (int, string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/mcp", bytes.NewBufferString(payload))
	setMCPHeaders(request, env.created.APIKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	return response.StatusCode, string(body)
}

func requireJSONRPCTopLevelError(t *testing.T, responseBody string) {
	t.Helper()

	jsonPayload := []byte(strings.TrimSpace(responseBody))
	if len(jsonPayload) == 0 || jsonPayload[0] != '{' {
		jsonPayload = nil
		for responseLine := range strings.SplitSeq(responseBody, "\n") {
			responseLine = strings.TrimSpace(responseLine)
			if !strings.HasPrefix(responseLine, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(responseLine, "data:"))
			if payload != "" {
				jsonPayload = []byte(payload)
			}
		}
	}

	var responseEnvelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(jsonPayload, &responseEnvelope); err != nil {
		t.Fatalf("expected a JSON-RPC response, got %s: %v", truncate(responseBody, 512), err)
	}
	jsonRPCError := bytes.TrimSpace(responseEnvelope.Error)
	if len(jsonRPCError) == 0 || bytes.Equal(jsonRPCError, []byte("null")) {
		t.Fatalf("expected top-level JSON-RPC error, got %s", truncate(responseBody, 512))
	}
}
