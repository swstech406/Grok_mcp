package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/store"
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

func TestJWTRejectsMalformedWrongSecretAndExpiredTokens(t *testing.T) {
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

	expiringToken, _, err := IssuePanelToken(testSecret, user, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if code := do(t, h, expiringToken); code != http.StatusUnauthorized {
		t.Fatalf("expired token should be rejected with 401, got %d", code)
	}
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

// TestJWTMissingTierReturnsInternalServerError 与 MCP 对齐：tier 缺失返回 500 且 body 含错误信息。
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
	if !strings.Contains(rec.Body.String(), "tier") {
		t.Fatalf("body should describe tier failure, got %q", rec.Body.String())
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
