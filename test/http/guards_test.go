package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	return bootIntegrationEnvWithStoreDecorator(t, cpa, globalLimit, perUserLimit, nil)
}

func bootIntegrationEnvWithStoreDecorator(
	t *testing.T,
	cpa *httptest.Server,
	globalLimit int,
	perUserLimit int,
	decorateStore func(*store.SQLiteStore) store.Store,
) *integrationEnv {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "guards.db"))
	if err != nil {
		t.Fatal(err)
	}
	requestStore := store.Store(st)
	if decorateStore != nil {
		requestStore = decorateStore(st)
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
		AuthProtector:         lowDifficultyAuthProtector(),
	}
	handler := app.BuildHTTPHandler(app.HTTPDependencies{
		Store:                    requestStore,
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

	registrationBody := buildRegistrationRequestBody(t, ts.URL, "guarduser", "password123", "")
	regReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewReader(registrationBody))
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

type crossMonthQuotaStore struct {
	*store.SQLiteStore

	mutex                sync.Mutex
	reservationTimes     []time.Time
	nextReservationIndex int
	releaseTime          time.Time
	reservedTokens       []store.SuccessQuotaReservation
	releasedTokens       []store.SuccessQuotaReservation
}

func TestHTTPRevokeSessionsReplacesCurrentToken(t *testing.T) {
	cpa := cpaMockSSE(t)
	defer cpa.Close()
	integrationEnvironment := bootIntegrationEnv(t, cpa)

	revokeRequest, _ := http.NewRequest(
		http.MethodPost,
		integrationEnvironment.ts.URL+"/panel/v1/me/revoke-sessions",
		nil,
	)
	revokeRequest.Header.Set("Authorization", "Bearer "+integrationEnvironment.login.Token)
	revokeResponse, err := http.DefaultClient.Do(revokeRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer revokeResponse.Body.Close()
	if revokeResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(revokeResponse.Body)
		t.Fatalf("revoke status=%d body=%s", revokeResponse.StatusCode, responseBody)
	}
	var replacementSession panel.SessionReplacementResponse
	if err := json.NewDecoder(revokeResponse.Body).Decode(&replacementSession); err != nil {
		t.Fatal(err)
	}
	if replacementSession.Token == "" {
		t.Fatal("revoke response did not include a replacement token")
	}

	requestStatus := func(token string) int {
		t.Helper()
		currentUserRequest, _ := http.NewRequest(
			http.MethodGet,
			integrationEnvironment.ts.URL+"/panel/v1/me",
			nil,
		)
		currentUserRequest.Header.Set("Authorization", "Bearer "+token)
		currentUserResponse, requestErr := http.DefaultClient.Do(currentUserRequest)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		currentUserResponse.Body.Close()
		return currentUserResponse.StatusCode
	}
	if status := requestStatus(integrationEnvironment.login.Token); status != http.StatusUnauthorized {
		t.Fatalf("previous token status=%d, want %d", status, http.StatusUnauthorized)
	}
	if status := requestStatus(replacementSession.Token); status != http.StatusOK {
		t.Fatalf("replacement token status=%d, want %d", status, http.StatusOK)
	}
}

func (quotaStore *crossMonthQuotaStore) ReserveSuccessCall(
	requestContext context.Context,
	userID string,
	successLimit int,
) (store.SuccessQuotaReservation, error) {
	quotaStore.mutex.Lock()
	if quotaStore.nextReservationIndex >= len(quotaStore.reservationTimes) {
		quotaStore.mutex.Unlock()
		return store.SuccessQuotaReservation{}, errors.New("unexpected extra quota reservation")
	}
	reservationTime := quotaStore.reservationTimes[quotaStore.nextReservationIndex]
	quotaStore.nextReservationIndex++
	quotaStore.mutex.Unlock()

	reservationContext := store.WithSuccessQuotaNow(requestContext, reservationTime)
	reservation, err := quotaStore.SQLiteStore.ReserveSuccessCall(reservationContext, userID, successLimit)
	if err != nil {
		return store.SuccessQuotaReservation{}, err
	}

	quotaStore.mutex.Lock()
	quotaStore.reservedTokens = append(quotaStore.reservedTokens, reservation)
	quotaStore.mutex.Unlock()
	return reservation, nil
}

func (quotaStore *crossMonthQuotaStore) ReleaseSuccessCall(
	requestContext context.Context,
	reservation store.SuccessQuotaReservation,
) error {
	quotaStore.mutex.Lock()
	quotaStore.releasedTokens = append(quotaStore.releasedTokens, reservation)
	quotaStore.mutex.Unlock()

	releaseContext := store.WithSuccessQuotaNow(requestContext, quotaStore.releaseTime)
	return quotaStore.SQLiteStore.ReleaseSuccessCall(releaseContext, reservation)
}

func (quotaStore *crossMonthQuotaStore) quotaTokens() (
	[]store.SuccessQuotaReservation,
	[]store.SuccessQuotaReservation,
) {
	quotaStore.mutex.Lock()
	defer quotaStore.mutex.Unlock()
	reservedTokens := append([]store.SuccessQuotaReservation(nil), quotaStore.reservedTokens...)
	releasedTokens := append([]store.SuccessQuotaReservation(nil), quotaStore.releasedTokens...)
	return reservedTokens, releasedTokens
}

func TestHTTPCrossMonthFailurePreservesLaterReservation(t *testing.T) {
	firstUpstreamStarted := make(chan struct{})
	releaseFirstUpstream := make(chan struct{})
	var releaseFirstUpstreamOnce sync.Once
	releaseFirstRequest := func() {
		releaseFirstUpstreamOnce.Do(func() { close(releaseFirstUpstream) })
	}
	defer releaseFirstRequest()
	var upstreamCalls atomic.Int64
	cpa := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		upstreamCallNumber := upstreamCalls.Add(1)
		switch upstreamCallNumber {
		case 1:
			if err := validateCPAMockRequest(request, newWebSearchCPAExpectation("january failure")); err != nil {
				t.Errorf("first CPA request was invalid: %v", err)
				http.Error(responseWriter, "invalid first CPA request", http.StatusBadRequest)
				return
			}
			close(firstUpstreamStarted)
			<-releaseFirstUpstream
			http.Error(responseWriter, "upstream unavailable", http.StatusBadGateway)
		case 2:
			if err := validateCPAMockRequest(request, newWebSearchCPAExpectation("february success")); err != nil {
				t.Errorf("second CPA request was invalid: %v", err)
				http.Error(responseWriter, "invalid second CPA request", http.StatusBadRequest)
				return
			}
			writeCPAMockSSEResponse(responseWriter, "february answer")
		default:
			t.Errorf("unexpected CPA call number %d", upstreamCallNumber)
			http.Error(responseWriter, "unexpected CPA request", http.StatusInternalServerError)
		}
	}))
	defer cpa.Close()

	januaryTime := time.Date(2026, time.January, 31, 23, 59, 0, 0, time.UTC)
	februaryTime := time.Date(2026, time.February, 1, 0, 1, 0, 0, time.UTC)
	var quotaStore *crossMonthQuotaStore
	environment := bootIntegrationEnvWithStoreDecorator(t, cpa, 16, 4, func(sqliteStore *store.SQLiteStore) store.Store {
		quotaStore = &crossMonthQuotaStore{
			SQLiteStore:      sqliteStore,
			reservationTimes: []time.Time{januaryTime, februaryTime},
			releaseTime:      februaryTime,
		}
		return quotaStore
	})

	firstRequestResult := make(chan struct {
		statusCode int
		body       string
		err        error
	}, 1)
	go func() {
		requestBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"january failure"}}}`
		request, _ := http.NewRequest(http.MethodPost, environment.ts.URL+"/mcp", bytes.NewBufferString(requestBody))
		setMCPHeaders(request, environment.created.APIKey)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			firstRequestResult <- struct {
				statusCode int
				body       string
				err        error
			}{err: err}
			return
		}
		responseBody, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		firstRequestResult <- struct {
			statusCode int
			body       string
			err        error
		}{statusCode: response.StatusCode, body: string(responseBody), err: readErr}
	}()

	select {
	case <-firstUpstreamStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for January request to reach the upstream")
	}

	secondRequestBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"february success"}}}`
	secondStatus, secondBody := callMCPTool(t, environment, secondRequestBody)
	if secondStatus != http.StatusOK || !strings.Contains(secondBody, "february answer") {
		t.Fatalf("February request status=%d body=%s", secondStatus, truncate(secondBody, 512))
	}

	releaseFirstRequest()
	firstResult := <-firstRequestResult
	if firstResult.err != nil {
		t.Fatalf("January request failed at HTTP transport: %v", firstResult.err)
	}
	if firstResult.statusCode != http.StatusOK || !strings.Contains(firstResult.body, `"isError":true`) {
		t.Fatalf("January failure status=%d body=%s", firstResult.statusCode, truncate(firstResult.body, 512))
	}

	reservedTokens, releasedTokens := quotaStore.quotaTokens()
	if len(reservedTokens) != 2 || reservedTokens[0].Period != "2026-01" || reservedTokens[1].Period != "2026-02" {
		t.Fatalf("reserved tokens = %+v, want January then February", reservedTokens)
	}
	if len(releasedTokens) != 1 || releasedTokens[0] != reservedTokens[0] {
		t.Fatalf("released tokens = %+v, want original January token %+v", releasedTokens, reservedTokens[0])
	}

	februaryContext := store.WithSuccessQuotaNow(context.Background(), februaryTime)
	user, err := environment.st.GetUserByID(februaryContext, environment.login.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if user.SuccessPeriod != "2026-02" || user.SuccessCalls != 1 {
		t.Fatalf("February reservation was altered: calls=%d period=%q", user.SuccessCalls, user.SuccessPeriod)
	}
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

func TestHTTPUserRPMRejectsBeforeSearchConcurrencyQuotaUsageAndUpstream(t *testing.T) {
	var upstreamCalls atomic.Int64
	cpa := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if err := validateCPAMockRequest(request, newWebSearchCPAExpectation("user rpm guard")); err != nil {
			t.Errorf("CPA mock received invalid request: %v", err)
			http.Error(responseWriter, "invalid CPA mock request", http.StatusBadRequest)
			return
		}
		upstreamCalls.Add(1)
		writeCPAMockSSEResponse(responseWriter, "user rpm answer")
	}))
	defer cpa.Close()

	environment := bootIntegrationEnv(t, cpa)
	userRPM := 1
	if _, err := environment.st.UpdateTier(
		context.Background(),
		environment.login.User.TierID,
		store.TierUpdates{RPM: &userRPM},
	); err != nil {
		t.Fatal(err)
	}

	toolPayload := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"user rpm guard"}}}`
	firstStatus, firstBody := callMCPTool(t, environment, toolPayload)
	if firstStatus != http.StatusOK || !strings.Contains(firstBody, "user rpm answer") {
		t.Fatalf("first tool call status=%d body=%s", firstStatus, truncate(firstBody, 512))
	}
	secondStatus, secondBody := callMCPTool(t, environment, toolPayload)
	if secondStatus != http.StatusTooManyRequests || !strings.Contains(secondBody, "rate limit exceeded") {
		t.Fatalf("second tool call status=%d body=%s", secondStatus, truncate(secondBody, 512))
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls.Load())
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
		t.Fatalf("usage calls = %d, want 1", usageStats.TotalCalls)
	}
	user, err := environment.st.GetUserByID(context.Background(), environment.login.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if user.SuccessCalls != 1 {
		t.Fatalf("reserved success calls = %d, want 1", user.SuccessCalls)
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

func TestHTTPMCPRejectsAmbiguousToolRoutingFields(t *testing.T) {
	var upstreamCalls atomic.Int64
	cpa := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		writeCPAMockSSEResponse(responseWriter, "unexpected ambiguous request")
	}))
	defer cpa.Close()

	environment := bootIntegrationEnv(t, cpa)
	ambiguousPayloads := []string{
		`{"jsonrpc":"2.0","id":201,"method":"tools/call","METHOD":"initialize","params":{"name":"grok_web_search","arguments":{"query":"must not reach upstream"}}}`,
		`{"jsonrpc":"2.0","id":202,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"must not reach upstream"}},"PARAMS":{"name":""}}`,
		`{"jsonrpc":"2.0","id":203,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"must not reach upstream"},"NAME":""}}`,
		`{"jsonrpc":"2.0","id":204,"method":"tools/call","params":{"name":"grok_web_search","arguments":{"query":"must not reach upstream"}},"params":{"unexpected":true}}`,
	}

	for _, ambiguousPayload := range ambiguousPayloads {
		statusCode, responseBody := callMCPTool(t, environment, ambiguousPayload)
		if statusCode != http.StatusBadRequest {
			t.Fatalf("ambiguous tools/call status = %d, want %d; body=%s", statusCode, http.StatusBadRequest, truncate(responseBody, 512))
		}
		if !strings.Contains(responseBody, "Ambiguous JSON-RPC tool routing fields") {
			t.Fatalf("unexpected ambiguous tools/call response: %s", truncate(responseBody, 512))
		}
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("ambiguous tools/call requests reached upstream %d times", upstreamCalls.Load())
	}

	environment.writer.Close()
	usageStats, err := environment.st.GetUsageStats(context.Background(), environment.created.Key.ID, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if usageStats.TotalCalls != 0 || usageStats.SuccessCalls != 0 {
		t.Fatalf("rejected ambiguous requests must not consume quota or create usage records, got %+v", usageStats)
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
