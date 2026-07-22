package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "jwt-secret-must-be-at-least-32-bytes!"

// jwtTestStore 打开一个临时 SQLite 库并预置一名 admin 用户。
// 同时创建第二个 admin 作为备份，使"降级/禁用首个 admin"的测试不会触发 ErrLastAdmin 守卫。
func jwtTestStore(t *testing.T) (*store.SQLiteStore, *store.User) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "jwt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(t.Context(), "admin", "hash", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "backup-admin", "hash", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	return st, user
}

// guardedHandler 返回一个固定返回 200 的 handler，用于断言中间件是否放行。
func guardedHandler(secret string, st store.Store) http.Handler {
	return JWTMiddleware(secret, st)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func do(t *testing.T, h http.Handler, token string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/panel/v1/me", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestJWTValidTokenAccepted 验证刚签发的 token 能通过中间件。
func TestJWTValidTokenAccepted(t *testing.T) {
	st, user := jwtTestStore(t)
	h := guardedHandler(testSecret, st)

	token, _, err := IssuePanelToken(testSecret, user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, token); code != http.StatusOK {
		t.Fatalf("expected 200 for fresh token, got %d", code)
	}
}

func TestJWTRejectsMalformedAndWrongSecretTokens(t *testing.T) {
	st, user := jwtTestStore(t)
	h := guardedHandler(testSecret, st)

	if code := do(t, h, "not-a-jwt"); code != http.StatusUnauthorized {
		t.Fatalf("malformed token should be rejected with 401, got %d", code)
	}

	wrongSecretToken, _, err := IssuePanelToken("wrong-secret-must-be-at-least-32-bytes!", user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, wrongSecretToken); code != http.StatusUnauthorized {
		t.Fatalf("wrong-secret token should be rejected with 401, got %d", code)
	}

}

func TestJWTRejectsAdversarialClaimsBeforeStoreAccess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	validClaims := jwt.MapClaims{
		"uid":      "adversarial-user",
		"sub":      "adversarial-user",
		"username": "adversarial",
		"role":     string(store.RoleUser),
		"tv":       int64(0),
		"exp":      now.Add(time.Hour).Unix(),
		"iat":      now.Unix(),
		"nbf":      now.Unix(),
		"iss":      jwtIssuer,
		"aud":      jwtAudience,
	}

	testCases := []struct {
		name          string
		signingMethod jwt.SigningMethod
		mutateClaims  func(jwt.MapClaims)
	}{
		{name: "missing expiration", mutateClaims: func(claims jwt.MapClaims) { delete(claims, "exp") }},
		{name: "missing issued at", mutateClaims: func(claims jwt.MapClaims) { delete(claims, "iat") }},
		{name: "missing not before", mutateClaims: func(claims jwt.MapClaims) { delete(claims, "nbf") }},
		{name: "missing user id", mutateClaims: func(claims jwt.MapClaims) { delete(claims, "uid") }},
		{name: "empty user id", mutateClaims: func(claims jwt.MapClaims) { claims["uid"] = "  " }},
		{name: "missing subject", mutateClaims: func(claims jwt.MapClaims) { delete(claims, "sub") }},
		{name: "contradictory identities", mutateClaims: func(claims jwt.MapClaims) { claims["sub"] = "another-user" }},
		{name: "negative token version", mutateClaims: func(claims jwt.MapClaims) { claims["tv"] = int64(-1) }},
		{name: "premature not before", mutateClaims: func(claims jwt.MapClaims) { claims["nbf"] = now.Add(2 * time.Minute).Unix() }},
		{name: "premature issued at", mutateClaims: func(claims jwt.MapClaims) { claims["iat"] = now.Add(2 * time.Minute).Unix() }},
		{name: "expired", mutateClaims: func(claims jwt.MapClaims) { claims["exp"] = now.Add(-2 * time.Minute).Unix() }},
		{name: "missing issuer", mutateClaims: func(claims jwt.MapClaims) { delete(claims, "iss") }},
		{name: "wrong issuer", mutateClaims: func(claims jwt.MapClaims) { claims["iss"] = "another-service" }},
		{name: "missing audience", mutateClaims: func(claims jwt.MapClaims) { delete(claims, "aud") }},
		{name: "wrong audience", mutateClaims: func(claims jwt.MapClaims) { claims["aud"] = "another-client" }},
		{name: "wrong algorithm", signingMethod: jwt.SigningMethodHS384},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			claims := cloneMapClaims(validClaims)
			if testCase.mutateClaims != nil {
				testCase.mutateClaims(claims)
			}
			signingMethod := testCase.signingMethod
			if signingMethod == nil {
				signingMethod = jwt.SigningMethodHS256
			}
			token := signMapClaimsForTest(t, signingMethod, claims)
			loader := &recordingPanelTokenLoader{}
			downstreamCallCount := 0
			handler := JWTMiddleware(testSecret, loader)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				downstreamCallCount++
			}))
			request := httptest.NewRequest(http.MethodGet, "/panel/v1/me", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			responseRecorder := httptest.NewRecorder()

			handler.ServeHTTP(responseRecorder, request)

			if responseRecorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusUnauthorized)
			}
			if responseRecorder.Body.String() != "unauthorized\n" {
				t.Fatalf("response body = %q, want generic unauthorized failure", responseRecorder.Body.String())
			}
			if loader.userLoadCount != 0 || loader.tierLoadCount != 0 {
				t.Fatalf("invalid token caused user/tier loads: %+v", loader)
			}
			if downstreamCallCount != 0 {
				t.Fatalf("invalid token reached downstream %d time(s)", downstreamCallCount)
			}
			if _, err := ParsePanelToken(testSecret, token); err == nil {
				t.Fatal("ParsePanelToken accepted token rejected by middleware contract")
			}
		})
	}
}

func TestJWTStrictParserPreservesZeroTokenVersion(t *testing.T) {
	now := time.Now().UTC()
	token := signMapClaimsForTest(t, jwt.SigningMethodHS256, jwt.MapClaims{
		"uid":  "zero-version-user",
		"sub":  "zero-version-user",
		"role": string(store.RoleUser),
		"tv":   int64(0),
		"exp":  now.Add(time.Hour).Unix(),
		"iat":  now.Unix(),
		"nbf":  now.Unix(),
		"iss":  jwtIssuer,
		"aud":  jwtAudience,
	})

	claims, err := ParsePanelToken(testSecret, token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.TokenVersion != 0 {
		t.Fatalf("token version = %d, want 0", claims.TokenVersion)
	}
}

func cloneMapClaims(source jwt.MapClaims) jwt.MapClaims {
	clonedClaims := make(jwt.MapClaims, len(source))
	for claimName, claimValue := range source {
		clonedClaims[claimName] = claimValue
	}
	return clonedClaims
}

func signMapClaimsForTest(t *testing.T, signingMethod jwt.SigningMethod, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(signingMethod, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

type recordingPanelTokenLoader struct {
	userLoadCount int
	tierLoadCount int
}

func (loader *recordingPanelTokenLoader) GetUserByID(context.Context, string) (*store.User, error) {
	loader.userLoadCount++
	return nil, store.ErrUserNotFound
}

func (loader *recordingPanelTokenLoader) GetTierByID(context.Context, string) (*store.Tier, error) {
	loader.tierLoadCount++
	return nil, store.ErrTierNotFound
}

// TestJWTRoleDowngradeInvalidatesToken 验证角色降级后，旧 token 因 role + tv 不匹配被拒签。
func TestJWTRoleDowngradeInvalidatesToken(t *testing.T) {
	st, user := jwtTestStore(t)
	h := guardedHandler(testSecret, st)

	token, _, err := IssuePanelToken(testSecret, user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, token); code != http.StatusOK {
		t.Fatalf("fresh token should pass, got %d", code)
	}

	// 模拟管理员将该 admin 降级为 user —— UpdateUser 会自增 token_version。
	role := store.RoleUser
	if _, err := st.UpdateUser(t.Context(), user.ID, store.UserUpdates{Role: &role}); err != nil {
		t.Fatal(err)
	}

	// 同一枚旧 token 现在应被拒签（401）。
	if code := do(t, h, token); code != http.StatusUnauthorized {
		t.Fatalf("downgraded token should be rejected with 401, got %d", code)
	}

	// 重新加载用户并签发新 token，应当通过。
	fresh, err := st.GetUserByID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	newToken, _, err := IssuePanelToken(testSecret, fresh, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, newToken); code != http.StatusOK {
		t.Fatalf("re-issued token for downgraded user should pass, got %d", code)
	}
}

// TestJWTExplicitRevokeInvalidatesToken 验证 revoke_tokens=true 强制下线。
func TestJWTExplicitRevokeInvalidatesToken(t *testing.T) {
	st, user := jwtTestStore(t)
	h := guardedHandler(testSecret, st)

	token, _, err := IssuePanelToken(testSecret, user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, token); code != http.StatusOK {
		t.Fatalf("fresh token should pass, got %d", code)
	}

	// 不改角色/启用状态，仅显式吊销。
	revoke := true
	if _, err := st.UpdateUser(t.Context(), user.ID, store.UserUpdates{RevokeTokens: &revoke}); err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, token); code != http.StatusUnauthorized {
		t.Fatalf("revoked token should be rejected with 401, got %d", code)
	}

	// 重新登录（新 token）应恢复访问。
	fresh, err := st.GetUserByID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.TokenVersion != 1 {
		t.Fatalf("expected token_version=1 after one revoke, got %d", fresh.TokenVersion)
	}
	newToken, _, err := IssuePanelToken(testSecret, fresh, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, newToken); code != http.StatusOK {
		t.Fatalf("token after re-login should pass, got %d", code)
	}
}

// TestJWTDisabledUserRejected 验证启用状态关闭后中间件返回 403。
func TestJWTDisabledUserRejected(t *testing.T) {
	st, user := jwtTestStore(t)
	h := guardedHandler(testSecret, st)

	token, _, err := IssuePanelToken(testSecret, user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := st.UpdateUser(t.Context(), user.ID, store.UserUpdates{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, token); code != http.StatusForbidden {
		t.Fatalf("disabled user should get 403, got %d", code)
	}
}

// TestJWTTierOnlyUpdateKeepsTokenValid 验证仅调整到已有内置 tier 不会误伤有效 token。
func TestJWTTierOnlyUpdateKeepsTokenValid(t *testing.T) {
	st, user := jwtTestStore(t)
	h := guardedHandler(testSecret, st)

	token, _, err := IssuePanelToken(testSecret, user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tier1, err := st.GetTierByName(t.Context(), "tier1")
	if err != nil || tier1 == nil {
		t.Fatalf("tier1 should be seeded by migration: %v", err)
	}
	// 仅改 tier_id 到已有内置 tier，不应自增 token_version。
	if _, err := st.UpdateUser(t.Context(), user.ID, store.UserUpdates{TierID: &tier1.ID}); err != nil {
		t.Fatal(err)
	}
	if code := do(t, h, token); code != http.StatusOK {
		t.Fatalf("tier-only update should not invalidate token, got %d", code)
	}
}

type jwtTierFailStore struct {
	store.TestStore
	user *store.User
	err  error
}

func (s jwtTierFailStore) GetUserByID(context.Context, string) (*store.User, error) {
	if s.user == nil {
		return nil, store.ErrUserNotFound
	}
	copyUser := *s.user
	return &copyUser, nil
}

func (s jwtTierFailStore) GetTierByID(context.Context, string) (*store.Tier, error) {
	return nil, s.err
}

func (s jwtTierFailStore) GetTierByName(context.Context, string) (*store.Tier, error) {
	return nil, nil
}

// TestJWTMissingTierReturnsInternalServerError 与 MCP 对齐：tier 缺失返回 500 且不泄露内部标识。
func TestJWTMissingTierReturnsInternalServerError(t *testing.T) {
	user := &store.User{ID: "u1", Username: "u", Role: store.RoleUser, Enabled: true, TierID: "missing"}
	st := jwtTierFailStore{user: user, err: store.ErrTierNotFound}
	token, _, err := IssuePanelToken(testSecret, user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := guardedHandler(testSecret, st)
	req := httptest.NewRequest(http.MethodGet, "/panel/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing tier must be 500, got %d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "authentication failed\n" {
		t.Fatalf("body should use a generic authentication failure, got %q", rec.Body.String())
	}
}

// TestJWTMissingUserReturnsUnauthorized 用户不存在时仍为 401 user not found。
func TestJWTMissingUserReturnsUnauthorized(t *testing.T) {
	user := &store.User{ID: "gone", Username: "u", Role: store.RoleUser, Enabled: true, TokenVersion: 0}
	token, _, err := IssuePanelToken(testSecret, user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	st := jwtTierFailStore{user: nil}
	h := guardedHandler(testSecret, st)
	req := httptest.NewRequest(http.MethodGet, "/panel/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing user must be 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "user not found") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
