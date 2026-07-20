package panel

import (
	"net/http"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
)

// NewMux 在一处组合公开、JWT 鉴权和管理员路由，避免调用方维护公开路径白名单。
func NewMux(handler *Handler) http.Handler {
	mux := http.NewServeMux()
	authProtector := handler.authProtector()
	mux.HandleFunc("GET /panel/v1/auth/registration-settings", handler.registrationSettings)
	mux.Handle("POST /panel/v1/auth/registration-challenge", authProtector.RateLimitAuthEndpoint(authEndpointRegistrationChallenge, http.HandlerFunc(handler.registrationChallenge)))
	mux.Handle("POST /panel/v1/auth/register", authProtector.RateLimitAuthEndpoint(authEndpointRegister, http.HandlerFunc(handler.register)))
	mux.Handle("POST /panel/v1/auth/login", authProtector.RateLimitAuthEndpoint(authEndpointLogin, http.HandlerFunc(handler.login)))

	authenticated := http.NewServeMux()
	authenticated.HandleFunc("GET /panel/v1/me", handler.me)
	authenticated.HandleFunc("GET /panel/v1/overview/health", handler.overviewHealth)
	authenticated.HandleFunc("GET /panel/v1/keys", handler.listKeys)
	authenticated.HandleFunc("POST /panel/v1/keys", handler.createKey)
	authenticated.HandleFunc("POST /panel/v1/keys/{id}/reveal", handler.revealKey)
	authenticated.HandleFunc("PATCH /panel/v1/keys/{id}", handler.updateKey)
	authenticated.HandleFunc("DELETE /panel/v1/keys/{id}", handler.deleteKey)
	authenticated.HandleFunc("GET /panel/v1/keys/{id}/usage", handler.keyUsage)
	authenticated.HandleFunc("GET /panel/v1/usage", handler.userUsage)
	authenticated.HandleFunc("GET /panel/v1/usage/records", handler.userUsageRecords)
	authenticated.HandleFunc("GET /panel/v1/usage/records/{id}", handler.usageRecordDetail)
	handler.registerAdminRoutes(authenticated)

	authenticatedHandler := auth.JWTMiddleware(handler.JWTSecret, handler.Store)(authenticated)
	mux.Handle("/panel/v1/", authenticatedHandler)
	mux.Handle("/panel/v1", authenticatedHandler)
	return mux
}

// registerAdminRoutes 在给定 mux 上注册管理员路由，并套上 RequireAdmin 中间件。
func (handler *Handler) registerAdminRoutes(mux *http.ServeMux) {
	admin := http.NewServeMux()
	admin.HandleFunc("GET /panel/v1/admin/users", handler.adminListUsers)
	admin.HandleFunc("GET /panel/v1/admin/users/{id}", handler.adminGetUser)
	admin.HandleFunc("PATCH /panel/v1/admin/users/{id}", handler.adminUpdateUser)
	admin.HandleFunc("DELETE /panel/v1/admin/users/{id}", handler.adminDeleteUser)
	admin.HandleFunc("GET /panel/v1/admin/users/{id}/usage", handler.adminUserUsage)
	admin.HandleFunc("GET /panel/v1/admin/tiers", handler.adminListTiers)
	admin.HandleFunc("POST /panel/v1/admin/tiers", handler.adminCreateTier)
	admin.HandleFunc("PATCH /panel/v1/admin/tiers/{id}", handler.adminUpdateTier)
	admin.HandleFunc("DELETE /panel/v1/admin/tiers/{id}", handler.adminDeleteTier)
	admin.HandleFunc("GET /panel/v1/admin/settings", handler.adminGetServerSettings)
	admin.HandleFunc("PATCH /panel/v1/admin/settings", handler.adminUpdateServerSettings)
	admin.HandleFunc("GET /panel/v1/admin/invite-codes", handler.adminListInviteCodes)
	admin.HandleFunc("POST /panel/v1/admin/invite-codes", handler.adminCreateInviteCode)
	admin.HandleFunc("GET /panel/v1/admin/invite-codes/{id}/redemptions", handler.adminListInviteCodeRedemptions)
	admin.HandleFunc("PATCH /panel/v1/admin/invite-codes/{id}", handler.adminUpdateInviteCode)
	admin.HandleFunc("DELETE /panel/v1/admin/invite-codes/{id}", handler.adminDeleteInviteCode)
	admin.HandleFunc("GET /panel/v1/admin/models", handler.adminListModels)
	admin.HandleFunc("GET /panel/v1/admin/operations/metrics", handler.adminOperationalMetrics)
	mux.Handle("/panel/v1/admin/", auth.RequireAdmin()(admin))
}
