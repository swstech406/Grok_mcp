package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func panelTestServer(t *testing.T) (*httptest.Server, *store.SQLiteStore, *config.Config) {
	return panelTestServerWithAuthProtector(t, nil)
}

func panelTestServerWithAuthProtector(t *testing.T, authProtector *AuthProtector) (*httptest.Server, *store.SQLiteStore, *config.Config) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		JWTSecret: "jwt-secret-must-be-at-least-32-bytes!",
	}
	if err := st.ConfigureAPIKeyEncryption(cfg.JWTSecret); err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		Store:         st,
		JWTSecret:     cfg.JWTSecret,
		AuthProtector: authProtector,
	}
	return httptest.NewServer(NewMux(h)), st, cfg
}

func panelTestServerWithModelLister(t *testing.T, modelLister ModelLister) (*httptest.Server, *store.SQLiteStore, *config.Config) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		JWTSecret: "jwt-secret-must-be-at-least-32-bytes!",
	}
	if err := st.ConfigureAPIKeyEncryption(cfg.JWTSecret); err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		Store:       st,
		JWTSecret:   cfg.JWTSecret,
		ModelLister: modelLister,
	}
	return httptest.NewServer(NewMux(h)), st, cfg
}

type staticModelLister struct {
	models []grok.Model
	err    error
}

func (l staticModelLister) ListModels(context.Context) ([]grok.Model, error) {
	return l.models, l.err
}

func withJWT(req *http.Request, jwt string) *http.Request {
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}
	return req
}

func TestUsageRecordDetailLoadsBodiesOnDemandAndEnforcesOwnership(t *testing.T) {
	testServer, sqliteStore, configuration := panelTestServer(t)
	defer testServer.Close()
	ctx := context.Background()

	owner, err := sqliteStore.CreateUser(ctx, "usage-detail-owner", "hash", store.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := sqliteStore.CreateUser(ctx, "usage-detail-other", "hash", store.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	administrator, err := sqliteStore.CreateUser(ctx, "usage-detail-admin", "hash", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	apiKey, _, err := sqliteStore.CreateKey(ctx, owner.ID, "usage-detail-key")
	if err != nil {
		t.Fatal(err)
	}

	const requestBody = `{"query":"private request"}`
	const responseBody = `{"answer":"private response"}`
	requestPath := filepath.Join(t.TempDir(), "request.body")
	responsePath := filepath.Join(t.TempDir(), "response.body")
	if err := os.WriteFile(requestPath, []byte(requestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(responsePath, []byte(responseBody), 0o600); err != nil {
		t.Fatal(err)
	}
	recordTimestamp := time.Now().UTC()
	if err := sqliteStore.RecordUsage(ctx, store.UsageRecord{
		KeyID: apiKey.ID, ToolName: "grok_web_search", Timestamp: recordTimestamp,
		DebugJSON: `{"version":2}`, DebugRequestBodyPath: requestPath, DebugResponseBodyPath: responsePath,
	}); err != nil {
		t.Fatal(err)
	}
	usageStats, err := sqliteStore.GetUsageStats(ctx, apiKey.ID, recordTimestamp.Add(-time.Minute))
	if err != nil || len(usageStats.Records) != 1 {
		t.Fatalf("usage stats = %+v, err = %v", usageStats, err)
	}
	usageID := usageStats.Records[0].ID

	issueToken := func(user *store.User) string {
		t.Helper()
		token, _, issueErr := auth.IssuePanelToken(configuration.JWTSecret, user, 0)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return token
	}
	requestUsage := func(path, token string) (int, string) {
		t.Helper()
		request, requestErr := http.NewRequest(http.MethodGet, testServer.URL+path, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response, requestErr := http.DefaultClient.Do(withJWT(request, token))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		var responseBuffer bytes.Buffer
		if _, requestErr := responseBuffer.ReadFrom(response.Body); requestErr != nil {
			t.Fatal(requestErr)
		}
		return response.StatusCode, responseBuffer.String()
	}

	ownerToken := issueToken(owner)
	listStatus, listBody := requestUsage("/panel/v1/usage", ownerToken)
	if listStatus != http.StatusOK {
		t.Fatalf("usage list status = %d, body = %s", listStatus, listBody)
	}
	if strings.Contains(listBody, "private request") || strings.Contains(listBody, "private response") || strings.Contains(listBody, `"debug_request_body"`) {
		t.Fatalf("usage list exposed complete debug bodies: %s", listBody)
	}

	detailPath := "/panel/v1/usage/records/" + strconv.FormatInt(usageID, 10)
	ownerStatus, ownerBody := requestUsage(detailPath, ownerToken)
	if ownerStatus != http.StatusOK || !strings.Contains(ownerBody, "private request") || !strings.Contains(ownerBody, "private response") {
		t.Fatalf("owner detail status = %d, body = %s", ownerStatus, ownerBody)
	}
	otherStatus, otherBody := requestUsage(detailPath, issueToken(otherUser))
	if otherStatus != http.StatusNotFound || strings.Contains(otherBody, "private request") || strings.Contains(otherBody, "private response") {
		t.Fatalf("other user detail status = %d, body = %s", otherStatus, otherBody)
	}
	adminStatus, adminBody := requestUsage(detailPath, issueToken(administrator))
	if adminStatus != http.StatusOK || !strings.Contains(adminBody, "private request") || !strings.Contains(adminBody, "private response") {
		t.Fatalf("admin detail status = %d, body = %s", adminStatus, adminBody)
	}
}

func TestUsageEndpointsApplyRequestedDatabasePageSizeAndCursor(t *testing.T) {
	testServer, sqliteStore, configuration := panelTestServer(t)
	defer testServer.Close()
	ctx := context.Background()

	administrator, err := sqliteStore.CreateUser(ctx, "usage-pagination-admin", "hash", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	targetUser, err := sqliteStore.CreateUser(ctx, "usage-pagination-target", "hash", store.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := sqliteStore.CreateUser(ctx, "usage-pagination-other", "hash", store.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	targetKey, _, err := sqliteStore.CreateKey(ctx, targetUser.ID, "target-key")
	if err != nil {
		t.Fatal(err)
	}
	otherKey, _, err := sqliteStore.CreateKey(ctx, otherUser.ID, "other-key")
	if err != nil {
		t.Fatal(err)
	}

	recordTimestamp := time.Now().UTC().Truncate(time.Second)
	for recordIndex := 0; recordIndex < 35; recordIndex++ {
		if err := sqliteStore.RecordUsage(ctx, store.UsageRecord{
			KeyID:      targetKey.ID,
			ToolName:   "grok_web_search",
			Timestamp:  recordTimestamp,
			DurationMs: int64(recordIndex),
			Success:    true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for recordIndex := 0; recordIndex < 5; recordIndex++ {
		if err := sqliteStore.RecordUsage(ctx, store.UsageRecord{
			KeyID:      otherKey.ID,
			ToolName:   "grok_x_search",
			Timestamp:  recordTimestamp,
			DurationMs: int64(recordIndex),
			Success:    true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	issueToken := func(user *store.User) string {
		t.Helper()
		token, _, issueErr := auth.IssuePanelToken(configuration.JWTSecret, user, 0)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return token
	}
	requestUsage := func(path, token string) UsageStatsResponse {
		t.Helper()
		request, requestErr := http.NewRequest(http.MethodGet, testServer.URL+path, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response, requestErr := http.DefaultClient.Do(withJWT(request, token))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			var responseBuffer bytes.Buffer
			_, _ = responseBuffer.ReadFrom(response.Body)
			t.Fatalf("GET %s returned %d: %s", path, response.StatusCode, responseBuffer.String())
		}
		var usageResponse UsageStatsResponse
		if decodeErr := json.NewDecoder(response.Body).Decode(&usageResponse); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return usageResponse
	}

	targetToken := issueToken(targetUser)
	firstPage := requestUsage("/panel/v1/usage?limit=10", targetToken)
	if firstPage.TotalCalls != 35 || len(firstPage.Records) != 10 || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("unexpected first user page: %+v", firstPage)
	}
	secondPage := requestUsage("/panel/v1/usage?limit=10&cursor="+firstPage.NextCursor, targetToken)
	if len(secondPage.Records) != 10 {
		t.Fatalf("second user page contains %d records, want 10", len(secondPage.Records))
	}
	firstPageRecordIDs := make(map[int64]struct{}, len(firstPage.Records))
	for _, record := range firstPage.Records {
		firstPageRecordIDs[record.ID] = struct{}{}
	}
	for _, record := range secondPage.Records {
		if _, duplicated := firstPageRecordIDs[record.ID]; duplicated {
			t.Fatalf("usage record %d appeared in both cursor pages", record.ID)
		}
	}

	adminToken := issueToken(administrator)
	adminPage := requestUsage("/panel/v1/admin/users/"+targetUser.ID+"/usage?limit=20", adminToken)
	if adminPage.TotalCalls != 35 || len(adminPage.Records) != 20 || !adminPage.HasMore {
		t.Fatalf("unexpected admin target-user page: %+v", adminPage)
	}
	completeAdminPage := requestUsage("/panel/v1/admin/users/"+targetUser.ID+"/usage?limit=100", adminToken)
	if completeAdminPage.TotalCalls != 35 || len(completeAdminPage.Records) != 35 || completeAdminPage.HasMore {
		t.Fatalf("unexpected 100-record admin page: %+v", completeAdminPage)
	}
}

func TestAdminUpdateUserRejectsSelfDisableAndDowngrade(t *testing.T) {
	ts, st, cfg := panelTestServer(t)
	defer ts.Close()

	currentAdmin, err := st.CreateUser(context.Background(), "self-update-admin", "hash", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.IssuePanelToken(cfg.JWTSecret, currentAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}

	disableRequest, _ := http.NewRequest(
		http.MethodPatch,
		ts.URL+"/panel/v1/admin/users/"+currentAdmin.ID,
		bytes.NewBufferString(`{"enabled":false}`),
	)
	disableResponse, err := http.DefaultClient.Do(withJWT(disableRequest, token))
	if err != nil {
		t.Fatal(err)
	}
	disableResponse.Body.Close()
	if disableResponse.StatusCode != http.StatusConflict {
		t.Fatalf("expected self-disable to return 409, got %d", disableResponse.StatusCode)
	}

	downgradeRequest, _ := http.NewRequest(
		http.MethodPatch,
		ts.URL+"/panel/v1/admin/users/"+currentAdmin.ID,
		bytes.NewBufferString(`{"role":"user"}`),
	)
	downgradeResponse, err := http.DefaultClient.Do(withJWT(downgradeRequest, token))
	if err != nil {
		t.Fatal(err)
	}
	downgradeResponse.Body.Close()
	if downgradeResponse.StatusCode != http.StatusConflict {
		t.Fatalf("expected self-downgrade to return 409, got %d", downgradeResponse.StatusCode)
	}

	adminAfterAttempts, err := st.GetUserByID(context.Background(), currentAdmin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !adminAfterAttempts.Enabled || adminAfterAttempts.Role != store.RoleAdmin {
		t.Fatalf("self update attempts must leave admin enabled, got enabled=%v role=%s", adminAfterAttempts.Enabled, adminAfterAttempts.Role)
	}
}

func TestAdminUpdateUserAllowsDisablingOtherAdminWhenCurrentAdminRemains(t *testing.T) {
	ts, st, cfg := panelTestServer(t)
	defer ts.Close()

	currentAdmin, err := st.CreateUser(context.Background(), "current-admin", "hash", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	targetAdmin, err := st.CreateUser(context.Background(), "target-admin", "hash", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.IssuePanelToken(cfg.JWTSecret, currentAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}

	request, _ := http.NewRequest(
		http.MethodPatch,
		ts.URL+"/panel/v1/admin/users/"+targetAdmin.ID,
		bytes.NewBufferString(`{"enabled":false}`),
	)
	response, err := http.DefaultClient.Do(withJWT(request, token))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected disabling another admin to return 200, got %d", response.StatusCode)
	}

	enabledAdminCount, err := st.CountEnabledAdmins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if enabledAdminCount != 1 {
		t.Fatalf("enabled admin count want 1 got %d", enabledAdminCount)
	}
}

func TestRegisterCreatesRegularUser(t *testing.T) {
	ts, _, _ := panelTestServer(t)
	defer ts.Close()

	body := `{"username":"alice","password":"password123"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status %d", resp.StatusCode)
	}
	var u UserResponse
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		t.Fatal(err)
	}
	if u.Role != store.RoleUser {
		t.Fatalf("expected regular user, got %s", u.Role)
	}
}

func TestRegisterWithoutHeadersSucceeds(t *testing.T) {
	ts, _, _ := panelTestServer(t)
	defer ts.Close()

	body := `{"username":"bob","password":"password123"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 without headers, got %d", resp.StatusCode)
	}
}

func TestLoginAndMe(t *testing.T) {
	ts, _, _ := panelTestServer(t)
	defer ts.Close()

	reg := `{"username":"carol","password":"password123"}`
	r1, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(reg))
	http.DefaultClient.Do(r1)

	login := `{"username":"carol","password":"password123"}`
	r2, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/login", bytes.NewBufferString(login))
	resp, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	var lr LoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	r3, _ := http.NewRequest(http.MethodGet, ts.URL+"/panel/v1/me", nil)
	r3 = withJWT(r3, lr.Token)
	resp3, err := http.DefaultClient.Do(r3)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("me status %d", resp3.StatusCode)
	}
}

func TestSecondUserIsNotAdmin(t *testing.T) {
	ts, _, _ := panelTestServer(t)
	defer ts.Close()

	for _, body := range []string{
		`{"username":"first","password":"password123"}`,
		`{"username":"second","password":"password123"}`,
	} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(body))
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
	}

	login := `{"username":"second","password":"password123"}`
	r2, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/login", bytes.NewBufferString(login))
	resp, _ := http.DefaultClient.Do(r2)
	var lr LoginResponse
	json.NewDecoder(resp.Body).Decode(&lr)
	resp.Body.Close()
	if lr.User.Role != store.RoleUser {
		t.Fatalf("expected user role, got %s", lr.User.Role)
	}
}

func TestRegisterRejectsOversizedPassword(t *testing.T) {
	ts, _, _ := panelTestServer(t)
	defer ts.Close()

	longPassword := strings.Repeat("a", maxPanelPasswordBytes+1)
	body := `{"username":"oversized","password":"` + longPassword + `"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized password, got %d", resp.StatusCode)
	}
}

func TestAuthEndpointRateLimitByIP(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginIPRequestsPerMinute:    100,
		LoginIPBurst:                100,
		RegisterIPRequestsPerMinute: 1,
		RegisterIPBurst:             1,
	})
	ts, _, _ := panelTestServerWithAuthProtector(t, authProtector)
	defer ts.Close()

	body := `{"username":"ratelimited","password":"short"}`
	firstRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(body))
	firstRequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	firstResponse, err := http.DefaultClient.Do(firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	firstResponse.Body.Close()
	if firstResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected first request to reach handler validation, got %d", firstResponse.StatusCode)
	}

	secondRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(body))
	secondRequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	secondResponse, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer secondResponse.Body.Close()
	if secondResponse.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after IP rate limit, got %d", secondResponse.StatusCode)
	}
	if secondResponse.Header.Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header on rate limited response")
	}
}

func TestLoginFailureLocksUsernameIPPair(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginIPRequestsPerMinute:    100,
		LoginIPBurst:                100,
		RegisterIPRequestsPerMinute: 100,
		RegisterIPBurst:             100,
		LoginFailureThreshold:       1,
		LoginBaseLockout:            time.Minute,
		LoginMaxLockout:             time.Minute,
	})
	ts, _, _ := panelTestServerWithAuthProtector(t, authProtector)
	defer ts.Close()

	registerBody := `{"username":"lockuser","password":"password123"}`
	registerRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(registerBody))
	registerResponse, err := http.DefaultClient.Do(registerRequest)
	if err != nil {
		t.Fatal(err)
	}
	registerResponse.Body.Close()
	if registerResponse.StatusCode != http.StatusCreated {
		t.Fatalf("expected registration before lockout test, got %d", registerResponse.StatusCode)
	}

	badLoginBody := `{"username":"lockuser","password":"wrongpass"}`
	badLoginRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/login", bytes.NewBufferString(badLoginBody))
	badLoginRequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	badLoginResponse, err := http.DefaultClient.Do(badLoginRequest)
	if err != nil {
		t.Fatal(err)
	}
	badLoginResponse.Body.Close()
	if badLoginResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected first bad login to fail credentials, got %d", badLoginResponse.StatusCode)
	}

	goodLoginBody := `{"username":"lockuser","password":"password123"}`
	goodLoginRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/login", bytes.NewBufferString(goodLoginBody))
	goodLoginRequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	goodLoginResponse, err := http.DefaultClient.Do(goodLoginRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer goodLoginResponse.Body.Close()
	if goodLoginResponse.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected lockout to reject login before bcrypt, got %d", goodLoginResponse.StatusCode)
	}
	if goodLoginResponse.Header.Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header on lockout response")
	}
}

func TestHeaderlessLoginFailureDoesNotCreateIPLockout(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		LoginIPRequestsPerMinute:    1,
		LoginIPBurst:                1,
		RegisterIPRequestsPerMinute: 1,
		RegisterIPBurst:             1,
		LoginFailureThreshold:       1,
		LoginBaseLockout:            time.Minute,
		LoginMaxLockout:             time.Minute,
	})
	testServer, _, _ := panelTestServerWithAuthProtector(t, authProtector)
	defer testServer.Close()

	registerBody := `{"username":"directuser","password":"password123"}`
	registerRequest, _ := http.NewRequest(http.MethodPost, testServer.URL+"/panel/v1/auth/register", bytes.NewBufferString(registerBody))
	registerResponse, err := http.DefaultClient.Do(registerRequest)
	if err != nil {
		t.Fatal(err)
	}
	registerResponse.Body.Close()
	if registerResponse.StatusCode != http.StatusCreated {
		t.Fatalf("expected headerless registration to succeed, got %d", registerResponse.StatusCode)
	}

	badLoginBody := `{"username":"directuser","password":"wrongpass"}`
	badLoginRequest, _ := http.NewRequest(http.MethodPost, testServer.URL+"/panel/v1/auth/login", bytes.NewBufferString(badLoginBody))
	badLoginResponse, err := http.DefaultClient.Do(badLoginRequest)
	if err != nil {
		t.Fatal(err)
	}
	badLoginResponse.Body.Close()
	if badLoginResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected headerless bad login to fail credentials, got %d", badLoginResponse.StatusCode)
	}

	goodLoginBody := `{"username":"directuser","password":"password123"}`
	goodLoginRequest, _ := http.NewRequest(http.MethodPost, testServer.URL+"/panel/v1/auth/login", bytes.NewBufferString(goodLoginBody))
	goodLoginResponse, err := http.DefaultClient.Do(goodLoginRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer goodLoginResponse.Body.Close()
	if goodLoginResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected headerless valid login to bypass IP lockout, got %d", goodLoginResponse.StatusCode)
	}
}

func TestRegisterRejectsDuplicateUsernameAndInvalidJSON(t *testing.T) {
	ts, _, _ := panelTestServer(t)
	defer ts.Close()

	registerPanelUser(t, ts, "duplicate", "password123")

	duplicateRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(`{"username":"duplicate","password":"password123"}`))
	duplicateResponse, err := http.DefaultClient.Do(duplicateRequest)
	if err != nil {
		t.Fatal(err)
	}
	duplicateResponse.Body.Close()
	if duplicateResponse.StatusCode != http.StatusConflict {
		t.Fatalf("expected duplicate username to return 409, got %d", duplicateResponse.StatusCode)
	}

	invalidJSONRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(`{"username"`))
	invalidJSONResponse, err := http.DefaultClient.Do(invalidJSONRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer invalidJSONResponse.Body.Close()
	if invalidJSONResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected invalid JSON to return 400, got %d", invalidJSONResponse.StatusCode)
	}
}

func TestKeyLifecycleSupportsOwnedRevealWithoutListingSecret(t *testing.T) {
	ts, _, _ := panelTestServer(t)
	defer ts.Close()

	registerPanelUser(t, ts, "keyowner", "password123")
	loginResponse := loginPanelUser(t, ts, "keyowner", "password123")

	createRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/keys", bytes.NewBufferString(`{"name":"primary"}`))
	createRequest = withJWT(createRequest, loginResponse.Token)
	createResponse, err := http.DefaultClient.Do(createRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer createResponse.Body.Close()
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("expected create key status 201, got %d", createResponse.StatusCode)
	}
	var created CreateKeyResponse
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Key.ID == "" || !strings.HasPrefix(created.APIKey, "grok_") {
		t.Fatalf("expected created key metadata and one-time raw key, got %+v", created)
	}

	listRequest, _ := http.NewRequest(http.MethodGet, ts.URL+"/panel/v1/keys", nil)
	listRequest = withJWT(listRequest, loginResponse.Token)
	listResponse, err := http.DefaultClient.Do(listRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer listResponse.Body.Close()
	var listed struct {
		Keys []KeyResponse `json:"keys"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if listResponse.StatusCode != http.StatusOK || len(listed.Keys) != 1 {
		t.Fatalf("expected one listed key, status=%d keys=%+v", listResponse.StatusCode, listed.Keys)
	}
	if listed.Keys[0].ID != created.Key.ID || listed.Keys[0].Name != "primary" {
		t.Fatalf("unexpected listed key: %+v", listed.Keys[0])
	}

	revealRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/keys/"+created.Key.ID+"/reveal", nil)
	revealRequest = withJWT(revealRequest, loginResponse.Token)
	revealResponse, err := http.DefaultClient.Do(revealRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer revealResponse.Body.Close()
	if revealResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected reveal key status 200, got %d", revealResponse.StatusCode)
	}
	var revealed RevealKeyResponse
	if err := json.NewDecoder(revealResponse.Body).Decode(&revealed); err != nil {
		t.Fatal(err)
	}
	if revealed.APIKey != created.APIKey {
		t.Fatalf("revealed key does not match the created key")
	}

	registerPanelUser(t, ts, "other-key-user", "password123")
	otherLoginResponse := loginPanelUser(t, ts, "other-key-user", "password123")
	otherRevealRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/keys/"+created.Key.ID+"/reveal", nil)
	otherRevealRequest = withJWT(otherRevealRequest, otherLoginResponse.Token)
	otherRevealResponse, err := http.DefaultClient.Do(otherRevealRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer otherRevealResponse.Body.Close()
	if otherRevealResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("expected another user to receive 404 when revealing the key, got %d", otherRevealResponse.StatusCode)
	}

	updatedName := "renamed"
	updateBody := `{"name":"` + updatedName + `","enabled":false}`
	updateRequest, _ := http.NewRequest(http.MethodPatch, ts.URL+"/panel/v1/keys/"+created.Key.ID, bytes.NewBufferString(updateBody))
	updateRequest = withJWT(updateRequest, loginResponse.Token)
	updateResponse, err := http.DefaultClient.Do(updateRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer updateResponse.Body.Close()
	if updateResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected update key status 200, got %d", updateResponse.StatusCode)
	}
	var updated KeyResponse
	if err := json.NewDecoder(updateResponse.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != updatedName || updated.Enabled {
		t.Fatalf("unexpected updated key: %+v", updated)
	}

	deleteRequest, _ := http.NewRequest(http.MethodDelete, ts.URL+"/panel/v1/keys/"+created.Key.ID, nil)
	deleteRequest = withJWT(deleteRequest, loginResponse.Token)
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("expected delete key status 204, got %d", deleteResponse.StatusCode)
	}
}

func TestAdminRoutesRequireAdminRole(t *testing.T) {
	ts, st, _ := panelTestServer(t)
	defer ts.Close()

	createPanelAdminUser(t, st, "firstadmin", "password123")
	registerPanelUser(t, ts, "plainuser", "password123")
	loginResponse := loginPanelUser(t, ts, "plainuser", "password123")

	request, _ := http.NewRequest(http.MethodGet, ts.URL+"/panel/v1/admin/users", nil)
	request = withJWT(request, loginResponse.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected non-admin admin route access to return 403, got %d", response.StatusCode)
	}
}

func TestAdminListModelsFiltersNonGrokModels(t *testing.T) {
	modelLister := staticModelLister{models: []grok.Model{
		{ID: "grok-4.3"},
		{ID: "claude-3"},
		{ID: " Grok-Beta "},
		{ID: "grok-imagine-image"},
		{ID: "grok-imagine-video"},
		{ID: "grok-video-preview"},
		{ID: "grok-4.3"},
	}}
	ts, st, _ := panelTestServerWithModelLister(t, modelLister)
	defer ts.Close()

	createPanelAdminUser(t, st, "modelsadmin", "password123")
	adminLogin := loginPanelUser(t, ts, "modelsadmin", "password123")

	request, _ := http.NewRequest(http.MethodGet, ts.URL+"/panel/v1/admin/models", nil)
	request = withJWT(request, adminLogin.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected model list status 200, got %d", response.StatusCode)
	}
	var modelsResponse ModelsResponse
	if err := json.NewDecoder(response.Body).Decode(&modelsResponse); err != nil {
		t.Fatal(err)
	}
	actualModelIDs := make([]string, 0, len(modelsResponse.Models))
	for _, model := range modelsResponse.Models {
		actualModelIDs = append(actualModelIDs, model.ID)
	}
	expectedModelIDs := []string{"grok-4.3", "Grok-Beta"}
	if len(actualModelIDs) != len(expectedModelIDs) {
		t.Fatalf("model IDs = %+v, want %+v", actualModelIDs, expectedModelIDs)
	}
	for index, expectedModelID := range expectedModelIDs {
		if actualModelIDs[index] != expectedModelID {
			t.Fatalf("model IDs = %+v, want %+v", actualModelIDs, expectedModelIDs)
		}
	}
}

func TestAdminRevokeTokensInvalidatesExistingJWT(t *testing.T) {
	ts, st, _ := panelTestServer(t)
	defer ts.Close()

	createPanelAdminUser(t, st, "tokenadmin", "password123")
	registerPanelUser(t, ts, "tokenuser", "password123")
	adminLogin := loginPanelUser(t, ts, "tokenadmin", "password123")
	userLogin := loginPanelUser(t, ts, "tokenuser", "password123")

	revokeRequest, _ := http.NewRequest(http.MethodPatch, ts.URL+"/panel/v1/admin/users/"+userLogin.User.ID, bytes.NewBufferString(`{"revoke_tokens":true}`))
	revokeRequest = withJWT(revokeRequest, adminLogin.Token)
	revokeResponse, err := http.DefaultClient.Do(revokeRequest)
	if err != nil {
		t.Fatal(err)
	}
	revokeResponse.Body.Close()
	if revokeResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected token revocation update status 200, got %d", revokeResponse.StatusCode)
	}

	meRequest, _ := http.NewRequest(http.MethodGet, ts.URL+"/panel/v1/me", nil)
	meRequest = withJWT(meRequest, userLogin.Token)
	meResponse, err := http.DefaultClient.Do(meRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer meResponse.Body.Close()
	if meResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected revoked token to return 401, got %d", meResponse.StatusCode)
	}
}

func registerPanelUser(t *testing.T, ts *httptest.Server, username string, password string) UserResponse {
	t.Helper()
	requestBody := `{"username":"` + username + `","password":"` + password + `"}`
	request, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(requestBody))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register %q status = %d, want %d", username, response.StatusCode, http.StatusCreated)
	}
	var userResponse UserResponse
	if err := json.NewDecoder(response.Body).Decode(&userResponse); err != nil {
		t.Fatal(err)
	}
	return userResponse
}

func createPanelAdminUser(t *testing.T, st store.Store, username string, password string) UserResponse {
	t.Helper()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(context.Background(), username, string(passwordHash), store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	return toUserResponse(user)
}

func loginPanelUser(t *testing.T, ts *httptest.Server, username string, password string) LoginResponse {
	t.Helper()
	requestBody := `{"username":"` + username + `","password":"` + password + `"}`
	request, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/login", bytes.NewBufferString(requestBody))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login %q status = %d, want %d", username, response.StatusCode, http.StatusOK)
	}
	var loginResponse LoginResponse
	if err := json.NewDecoder(response.Body).Decode(&loginResponse); err != nil {
		t.Fatal(err)
	}
	return loginResponse
}
