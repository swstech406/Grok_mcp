package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/grok-mcp/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

const defaultJWTExpiry = 12 * time.Hour

type panelClaims struct {
	UserID   string          `json:"uid"`
	Username string          `json:"username"`
	Role     store.UserRole  `json:"role"`
	jwt.RegisteredClaims
}

// IssuePanelToken 签发 HS256 面板 JWT。
func IssuePanelToken(secret string, user *store.User, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = defaultJWTExpiry
	}
	exp := time.Now().UTC().Add(ttl)
	claims := panelClaims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	return signed, exp, err
}

// JWTMiddleware 校验 Bearer JWT 并将用户摘要写入 context；对 skip 中的路径（注册/登录）不校验 JWT。
func JWTMiddleware(secret string, st store.Store, skip map[string]struct{}) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skip[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
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
			})
			if err != nil {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}
			user, err := st.GetUserByID(r.Context(), claims.UserID)
			if err != nil {
				http.Error(w, "user not found", http.StatusUnauthorized)
				return
			}
			if !user.Enabled {
				http.Error(w, "user disabled", http.StatusForbidden)
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

// ParsePanelToken 供测试解析 JWT。
func ParsePanelToken(secret, tokenStr string) (*panelClaims, error) {
	claims := &panelClaims{}
	_, err := jwt.ParseWithClaims(strings.TrimSpace(tokenStr), claims, func(t *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	return claims, err
}