// Package auth 提供 HTTP Bearer 鉴权：面向 MCP 的 API Key 与面向 /admin 的静态管理令牌。
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

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

// hashToken 对明文 API Key 做 SHA-256，与库中仅存 hash 的设计一致。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// APIKeyMiddleware 校验 Bearer 是否为已启用密钥，成功后将 *store.APIKey 写入请求 context。
func APIKeyMiddleware(st store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
				return
			}

			key, err := st.GetKeyByHash(r.Context(), hashToken(token))
			if err != nil {
				http.Error(w, "authentication failed", http.StatusInternalServerError)
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

			next.ServeHTTP(w, r.WithContext(WithAPIKey(r.Context(), key)))
		})
	}
}

// AdminTokenMiddleware 用配置中的 GROK_ADMIN_TOKEN 保护管理 API；使用恒定时间比较以防时序侧信道。
func AdminTokenMiddleware(adminToken string) func(http.Handler) http.Handler {
	expected := strings.TrimSpace(adminToken)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
