// Package auth 提供 HTTP Bearer 鉴权：MCP API Key 与面板 JWT。
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/grok-mcp/internal/keyhash"
	"github.com/grok-mcp/internal/store"
)

// bearerToken 从 Authorization: Bearer <token> 头解析令牌。
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", false
	}
	return token, true
}

// APIKeyResolver 解析 Bearer 令牌对应的 API Key 与所属用户（含 tier 限额）。
type APIKeyResolver interface {
	Resolve(ctx context.Context, keyHash string) (key *store.APIKey, user *store.User, err error)
}

type storeAPIKeyResolver struct {
	st store.Store
}

func (s storeAPIKeyResolver) Resolve(ctx context.Context, keyHash string) (*store.APIKey, *store.User, error) {
	key, err := s.st.GetKeyByHash(ctx, keyHash)
	if err != nil || key == nil {
		return key, nil, err
	}
	user, err := LoadUserWithTierLimits(ctx, s.st, key.UserID)
	if err != nil {
		return nil, nil, err
	}
	return key, user, nil
}

// NewStoreAPIKeyResolver 使用 Store 直接解析（无缓存）。
func NewStoreAPIKeyResolver(st store.Store) APIKeyResolver {
	return storeAPIKeyResolver{st: st}
}

// APIKeyMiddleware 校验 Bearer 是否为已启用密钥，成功后将 *store.APIKey 写入请求 context。
func APIKeyMiddleware(resolver APIKeyResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
				return
			}

			key, user, err := resolver.Resolve(r.Context(), keyhash.HashAPIKey(token))
			if err != nil {
				http.Error(w, "authentication failed", http.StatusInternalServerError)
				return
			}
			if key == nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if !key.Enabled {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if !user.Enabled {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			ctx := WithAPIKey(r.Context(), key)
			ctx = WithUser(ctx, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
