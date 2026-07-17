// Package auth 提供 HTTP Bearer 鉴权：MCP API Key 与面板 JWT。
package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/keyhash"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

// KeyLookup loads an API key by its hash.
// Defined at the consumer side so auth does not require the full store.Store surface.
type KeyLookup interface {
	GetKeyByHash(ctx context.Context, hash string) (*store.APIKey, error)
}

// APIKeyStore is the minimal store surface for uncached API key resolution.
type APIKeyStore interface {
	KeyLookup
	UserTierLoader
}

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
	Resolve(ctx context.Context, keyHash string) (key *store.APIKey, user *AuthenticatedUser, err error)
}

type storeAPIKeyResolver struct {
	st APIKeyStore
}

func (s storeAPIKeyResolver) Resolve(ctx context.Context, keyHash string) (*store.APIKey, *AuthenticatedUser, error) {
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
func NewStoreAPIKeyResolver(st APIKeyStore) APIKeyResolver {
	return storeAPIKeyResolver{st: st}
}

// writeAuthLoadError 统一 JWT / MCP 鉴权在加载用户+tier 失败时的 HTTP 语义：
// - 用户不存在 → 401 + "user not found"
// - tier 缺失（含默认 tier0 / 已分配 tier）→ 500 + 错误信息（fail-closed，避免 0 限额被当成不限）
// - 其它存储错误 → 500 + "authentication failed"
func writeAuthLoadError(w http.ResponseWriter, err error, logPrefix string) {
	if err == nil {
		return
	}
	if errors.Is(err, store.ErrUserNotFound) {
		http.Error(w, "user not found", http.StatusUnauthorized)
		return
	}
	if errors.Is(err, store.ErrTierNotFound) {
		log.Printf("%s: tier unavailable: %v", logPrefix, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("%s: authentication load failed: %v", logPrefix, err)
	http.Error(w, "authentication failed", http.StatusInternalServerError)
}

// APIKeyMiddleware 校验 Bearer 是否为已启用密钥，成功后将 API Key 与 AuthenticatedUser 写入 context。
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
				writeAuthLoadError(w, err, "mcp auth")
				return
			}
			if key == nil {
				http.Error(w, "invalid API key", http.StatusForbidden)
				return
			}
			if !key.Enabled {
				http.Error(w, "API key disabled", http.StatusForbidden)
				return
			}
			if !user.Enabled {
				http.Error(w, "user disabled", http.StatusForbidden)
				return
			}

			ctx := WithAPIKey(r.Context(), key)
			ctx = WithUser(ctx, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
