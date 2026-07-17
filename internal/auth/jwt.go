package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultJWTExpiry = 12 * time.Hour
	// jwtIssuer / jwtAudience 用于绑定 token 的签发方与受众，
	// 防止同一密钥签发的 token 被跨服务/跨端复用。
	jwtIssuer   = "grok-mcp"
	jwtAudience = "panel"
)

type panelClaims struct {
	UserID   string         `json:"uid"`
	Username string         `json:"username"`
	Role     store.UserRole `json:"role"`
	// TokenVersion 镜像 DB 的 token_version；中间件比对二者，不一致即拒签，
	// 从而在角色变更/显式吊销后令存量 token 立即失效，无需黑名单。
	TokenVersion int64 `json:"tv"`
	jwt.RegisteredClaims
}

// IssuePanelToken 签发 HS256 面板 JWT，固定 iss=grok-mcp、aud=panel。
func IssuePanelToken(secret string, user *store.User, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = defaultJWTExpiry
	}
	now := time.Now().UTC()
	exp := now.Add(ttl)
	claims := panelClaims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    jwtIssuer,
			Audience:  []string{jwtAudience},
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	return signed, exp, err
}

// JWTMiddleware 校验 Bearer JWT 并将 AuthenticatedUser 写入 context。
// st 仅需满足 UserTierLoader（加载用户 + tier 限额），不必依赖完整 store.Store。
func JWTMiddleware(secret string, st UserTierLoader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr, ok := bearerToken(r)
			if !ok {
				http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
				return
			}
			claims := &panelClaims{}
			_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
				if t.Method != jwt.SigningMethodHS256 {
					return nil, errors.New("unexpected signing method")
				}
				return []byte(secret), nil
			}, jwt.WithIssuer(jwtIssuer), jwt.WithAudience(jwtAudience))
			if err != nil {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}
			user, err := LoadUserWithTierLimits(r.Context(), st, claims.UserID)
			if err != nil {
				// 与 MCP APIKeyMiddleware 对齐：用户缺失 401，tier 缺失 500（不伪装成 user not found）。
				writeAuthLoadError(w, err, "panel jwt")
				return
			}
			if !user.Enabled {
				http.Error(w, "user disabled", http.StatusForbidden)
				return
			}
			// 防御纵深：token 中的角色或版本号与 DB 当前值不一致即拒签。
			// 角色校验覆盖降级场景（admin→user）；版本号校验覆盖显式吊销/启用变更。
			if claims.Role != user.Role || claims.TokenVersion != user.TokenVersion {
				http.Error(w, "token revoked", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
		})
	}
}

// RequireAdmin 要求当前用户为 admin，无论路径如何。用于显式包裹 admin 子路由器，
// 替代基于路径前缀的判断：新增 admin 路由时必须挂到该子路由器才会被放行，
// 避免因前缀不匹配而静默绕过鉴权。
func RequireAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if user.Role != store.RoleAdmin {
				http.Error(w, "admin required", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ParsePanelToken 供测试解析 JWT（含 iss/aud 校验）。
func ParsePanelToken(secret, tokenStr string) (*panelClaims, error) {
	claims := &panelClaims{}
	_, err := jwt.ParseWithClaims(strings.TrimSpace(tokenStr), claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	}, jwt.WithIssuer(jwtIssuer), jwt.WithAudience(jwtAudience))
	return claims, err
}
