package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/grok"
	"github.com/grok-mcp/internal/store"
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
	firstResponse, err := http.DefaultClient.Do(firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	firstResponse.Body.Close()
	if firstResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected first request to reach handler validation, got %d", firstResponse.StatusCode)
	}

	secondRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/panel/v1/auth/register", bytes.NewBufferString(body))
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

func TestKeyLifecycleOnlyReturnsRawKeyOnCreate(t *testing.T) {
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
