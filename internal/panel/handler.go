package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Handler 实现面板 API；路由由 NewMux 注册。
type Handler struct {
	Store                 store.Store
	JWTSecret             string
	TrustedProxies        []*net.IPNet
	InitialServerSettings config.ServerSettings
	SettingsApplier       ServerSettingsApplier // 可选；保存服务器设置后热更新上游客户端
	ModelLister           ModelLister           // 可选；管理员面板通过它从上游拉取可用 Grok 模型
	AuthCache             AuthCacheInvalidator  // 可选；管理员变更用户/等级/密钥后清空 MCP 鉴权缓存
	AuthProtector         *AuthProtector        // 可选；未设置时使用内置面板登录/注册防护
}

// ServerSettingsApplier 接收热更新后的上游连接设置。
type ServerSettingsApplier interface {
	ApplyServerSettings(config.ServerSettings) error
}

// NewMux 在一处组合公开、JWT 鉴权和管理员路由，避免调用方维护公开路径白名单。
func NewMux(h *Handler) http.Handler {
	mux := http.NewServeMux()
	authProtector := h.authProtector()
	mux.HandleFunc("GET /panel/v1/auth/registration-settings", h.registrationSettings)
	mux.Handle("POST /panel/v1/auth/register", authProtector.RateLimitAuthEndpoint(authEndpointRegister, http.HandlerFunc(h.register)))
	mux.Handle("POST /panel/v1/auth/login", authProtector.RateLimitAuthEndpoint(authEndpointLogin, http.HandlerFunc(h.login)))

	authenticated := http.NewServeMux()
	authenticated.HandleFunc("GET /panel/v1/me", h.me)
	authenticated.HandleFunc("GET /panel/v1/keys", h.listKeys)
	authenticated.HandleFunc("POST /panel/v1/keys", h.createKey)
	authenticated.HandleFunc("POST /panel/v1/keys/{id}/reveal", h.revealKey)
	authenticated.HandleFunc("PATCH /panel/v1/keys/{id}", h.updateKey)
	authenticated.HandleFunc("DELETE /panel/v1/keys/{id}", h.deleteKey)
	authenticated.HandleFunc("GET /panel/v1/keys/{id}/usage", h.keyUsage)
	authenticated.HandleFunc("GET /panel/v1/usage", h.userUsage)
	h.registerAdminRoutes(authenticated)

	authenticatedHandler := auth.JWTMiddleware(h.JWTSecret, h.Store)(authenticated)
	mux.Handle("/panel/v1/", authenticatedHandler)
	mux.Handle("/panel/v1", authenticatedHandler)
	return mux
}

// registerAdminRoutes 在给定 mux 上注册管理员路由，并套上 RequireAdmin 中间件。
func (h *Handler) registerAdminRoutes(mux *http.ServeMux) {
	admin := http.NewServeMux()
	admin.HandleFunc("GET /panel/v1/admin/users", h.adminListUsers)
	admin.HandleFunc("GET /panel/v1/admin/users/{id}", h.adminGetUser)
	admin.HandleFunc("PATCH /panel/v1/admin/users/{id}", h.adminUpdateUser)
	admin.HandleFunc("DELETE /panel/v1/admin/users/{id}", h.adminDeleteUser)
	admin.HandleFunc("GET /panel/v1/admin/users/{id}/usage", h.adminUserUsage)
	admin.HandleFunc("GET /panel/v1/admin/tiers", h.adminListTiers)
	admin.HandleFunc("POST /panel/v1/admin/tiers", h.adminCreateTier)
	admin.HandleFunc("PATCH /panel/v1/admin/tiers/{id}", h.adminUpdateTier)
	admin.HandleFunc("DELETE /panel/v1/admin/tiers/{id}", h.adminDeleteTier)
	admin.HandleFunc("GET /panel/v1/admin/settings", h.adminGetServerSettings)
	admin.HandleFunc("PATCH /panel/v1/admin/settings", h.adminUpdateServerSettings)
	admin.HandleFunc("GET /panel/v1/admin/invite-codes", h.adminListInviteCodes)
	admin.HandleFunc("POST /panel/v1/admin/invite-codes", h.adminCreateInviteCode)
	admin.HandleFunc("PATCH /panel/v1/admin/invite-codes/{id}", h.adminUpdateInviteCode)
	admin.HandleFunc("DELETE /panel/v1/admin/invite-codes/{id}", h.adminDeleteInviteCode)
	admin.HandleFunc("GET /panel/v1/admin/models", h.adminListModels)
	mux.Handle("/panel/v1/admin/", auth.RequireAdmin()(admin))
}

// parseSince 解析 ?since=RFC3339 查询参数；raw 非空但格式非法时返回 (zero, false)，
// 调用方应据此返回 400，避免静默回退到全量查询导致 usage_log 全表扫描。
func parseSince(r *http.Request) (time.Time, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("since"))
	if raw == "" {
		return time.Time{}, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
			return false
		}
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

const maxPanelBodyBytes = 1 << 20 // 1 MiB

// MaxPanelBodyBytes 返回面板 API 默认请求体上限。
func MaxPanelBodyBytes() int64 { return maxPanelBodyBytes }

// bcryptCost 为密码哈希工作因子；12 为当前常见基线（DefaultCost=10 在 GPU 离线破解下偏快）。
const bcryptCost = 12

// MaxBodyMiddleware 限制 JSON 请求体大小，防止恶意超大请求耗尽内存。
func MaxBodyMiddleware(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// loadUserTierForResponse 解析用户所属 tier；失败时打日志并返回 nil，
// 由 toUserResponseWithTier 标记 limits_unavailable，避免把零值限额显示成不限。
func (h *Handler) loadUserTierForResponse(ctx context.Context, user *store.User) *store.Tier {
	if user == nil {
		return nil
	}
	tierID := strings.TrimSpace(user.TierID)
	if tierID == "" {
		log.Printf("user %s has empty tier_id; limits unavailable", user.ID)
		return nil
	}
	tier, err := h.Store.GetTierByID(ctx, tierID)
	if err != nil {
		if errors.Is(err, store.ErrTierNotFound) {
			log.Printf("user %s tier_id %q not found; limits unavailable", user.ID, tierID)
			return nil
		}
		log.Printf("user %s load tier %q failed: %v; limits unavailable", user.ID, tierID, err)
		return nil
	}
	if tier == nil {
		log.Printf("user %s tier_id %q returned nil; limits unavailable", user.ID, tierID)
		return nil
	}
	return tier
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	registrationMode, err := h.currentRegistrationMode(r)
	if err != nil {
		log.Printf("load registration mode before register failed: %v", err)
		writeError(w, http.StatusInternalServerError, "registration failed")
		return
	}
	if registrationMode == store.RegistrationModeDisabled {
		writeError(w, http.StatusForbidden, "registration is disabled")
		return
	}
	username, err := validatePanelAuthCredentials(req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if existingUser, err := h.Store.GetUserByUsername(r.Context(), username); err != nil {
		log.Printf("check user %q before register failed: %v", username, err)
		writeError(w, http.StatusInternalServerError, "registration failed")
		return
	} else if existingUser != nil {
		writeError(w, http.StatusConflict, "username already taken")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hash failed")
		return
	}
	var user *store.User
	if registrationMode == store.RegistrationModeInvite {
		user, err = h.Store.RegisterUserWithInviteCode(r.Context(), username, string(hash), req.InviteCode)
	} else {
		user, err = h.Store.RegisterUser(r.Context(), username, string(hash))
	}
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			writeError(w, http.StatusConflict, "username already taken")
			return
		}
		if errors.Is(err, store.ErrInviteCodeInvalid) {
			writeError(w, http.StatusBadRequest, "valid invite code is required")
			return
		}
		if errors.Is(err, store.ErrInviteCodeDisabled) {
			writeError(w, http.StatusForbidden, "invite code is disabled")
			return
		}
		if errors.Is(err, store.ErrInviteCodeExhausted) {
			writeError(w, http.StatusForbidden, "invite code registration limit reached")
			return
		}
		log.Printf("register user %q failed: %v", username, err)
		writeError(w, http.StatusBadRequest, "registration failed")
		return
	}
	writeJSON(w, http.StatusCreated, toUserResponseWithTier(user, h.loadUserTierForResponse(r.Context(), user)))
}

func (h *Handler) registrationSettings(w http.ResponseWriter, r *http.Request) {
	registrationMode, err := h.currentRegistrationMode(r)
	if err != nil {
		log.Printf("load registration settings failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load registration settings")
		return
	}
	writeJSON(w, http.StatusOK, RegistrationSettingsResponse{RegistrationMode: registrationMode})
}

func (h *Handler) currentRegistrationMode(r *http.Request) (store.RegistrationMode, error) {
	storedSettings, err := h.Store.GetServerSettings(r.Context())
	if err != nil {
		return "", err
	}
	if storedSettings != nil {
		return store.NormalizeRegistrationMode(storedSettings.RegistrationMode)
	}
	if h.InitialServerSettings.RegistrationMode == "" {
		return store.RegistrationModeFree, nil
	}
	return store.NormalizeRegistrationMode(h.InitialServerSettings.RegistrationMode)
}

// dummyBcryptHash 是用于拉平登录时序的固定 bcrypt 哈希。
// 它在包初始化时用 bcryptCost 生成（与 register 一致的成本因子），
// 保证 CompareHashAndPassword 会执行完整的密钥派生流程，耗时可与真实用户哈希相当。
// 其明文密码无关紧要——仅用于在用户不存在时消耗相近的 CPU 时间。
var dummyBcryptHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("grok-mcp-timing-dummy-password"), bcryptCost)
	if err != nil {
		// 理论上不会失败；兜底返回一个空切片，此时退化为原始的快速失败行为。
		return nil
	}
	return h
}()

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	username, err := validatePanelAuthCredentials(req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	authProtector := h.authProtector()
	clientIP := authProtector.clientIP(r)
	if locked, retryAfter := authProtector.LoginLocked(username, clientIP); locked {
		writeRetryAfter(w, retryAfter)
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts")
		return
	}
	user, err := h.Store.GetUserByUsername(r.Context(), username)
	// 用户不存在时也执行一次 dummy bcrypt 比较，以拉平响应时间，避免通过时序差异枚举有效用户名。
	hashToCheck := dummyBcryptHash
	if err == nil && user != nil {
		hashToCheck = []byte(user.PasswordHash)
	}
	compareErr := bcrypt.CompareHashAndPassword(hashToCheck, []byte(req.Password))
	if err != nil || user == nil {
		// 用户不存在：上面已执行 dummy 比较，这里统一返回未授权，时序与存在用户分支一致。
		authProtector.RecordLoginFailure(username, clientIP)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if compareErr != nil {
		authProtector.RecordLoginFailure(username, clientIP)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !user.Enabled {
		authProtector.RecordLoginFailure(username, clientIP)
		writeError(w, http.StatusForbidden, "user disabled")
		return
	}
	authProtector.RecordLoginSuccess(username, clientIP)
	token, exp, err := auth.IssuePanelToken(h.JWTSecret, user, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: token, ExpiresAt: exp, User: toUserResponseWithTier(user, h.loadUserTierForResponse(r.Context(), user)),
	})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	fresh, err := h.Store.GetUserByID(r.Context(), user.ID)
	if err != nil {
		log.Printf("get user %s failed: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	writeJSON(w, http.StatusOK, toUserResponseWithTier(fresh, h.loadUserTierForResponse(r.Context(), fresh)))
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	keys, err := h.Store.ListKeysByUser(r.Context(), user.ID)
	if err != nil {
		log.Printf("list keys for user %s failed: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to load keys")
		return
	}
	out := make([]KeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req CreateKeyRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	k, raw, err := h.Store.CreateKey(r.Context(), user.ID, req.Name)
	if err != nil {
		log.Printf("create key for user %s failed: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}
	writeJSON(w, http.StatusCreated, CreateKeyResponse{Key: toKeyResponse(k), APIKey: raw})
}

func (h *Handler) revealKey(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	apiKey, err := h.Store.GetKeyByID(r.Context(), id)
	if err != nil || apiKey.UserID != user.ID {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	rawKey, err := h.Store.RevealKey(r.Context(), id)
	if err != nil {
		log.Printf("reveal key %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to reveal key")
		return
	}
	writeJSON(w, http.StatusOK, RevealKeyResponse{APIKey: rawKey})
}

func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	k, err := h.Store.GetKeyByID(r.Context(), id)
	if err != nil || k.UserID != user.ID {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	var req UpdateKeyRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name must not be empty")
		return
	}
	updated, err := h.Store.UpdateKey(r.Context(), id, store.KeyUpdates{Name: req.Name, Enabled: req.Enabled})
	if err != nil {
		log.Printf("update key %s failed: %v", id, err)
		writeError(w, http.StatusBadRequest, "failed to update key")
		return
	}
	h.invalidateAuthCache()
	writeJSON(w, http.StatusOK, toKeyResponse(updated))
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	k, err := h.Store.GetKeyByID(r.Context(), id)
	if err != nil || k.UserID != user.ID {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	if err := h.Store.DeleteKey(r.Context(), id); err != nil {
		log.Printf("delete key %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to delete key")
		return
	}
	h.invalidateAuthCache()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) keyUsage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	k, err := h.Store.GetKeyByID(r.Context(), id)
	if err != nil || k.UserID != user.ID {
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	since, ok := parseSince(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid 'since' query parameter; expected RFC3339")
		return
	}
	stats, err := h.Store.GetUsageStats(r.Context(), id, since)
	if err != nil {
		log.Printf("usage stats for key %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	writeJSON(w, http.StatusOK, toUsageStatsResponse(stats))
}

func (h *Handler) userUsage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	since, ok := parseSince(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid 'since' query parameter; expected RFC3339")
		return
	}
	stats, err := h.Store.GetUserUsageStats(r.Context(), user.ID, since)
	if err != nil {
		log.Printf("usage stats for user %s failed: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	writeJSON(w, http.StatusOK, toUsageStatsResponse(stats))
}

func (h *Handler) adminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.Store.ListUsers(r.Context())
	if err != nil {
		log.Printf("admin list users failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load users")
		return
	}
	tiers, err := h.Store.ListTiers(r.Context())
	if err != nil {
		log.Printf("admin list tiers while listing users failed: %v", err)
		// 继续返回用户列表，但 limits 将标记 unavailable。
		tiers = nil
	}
	tierByID := make(map[string]*store.Tier, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID] = t
	}
	out := make([]UserResponse, 0, len(users))
	for _, u := range users {
		tier := tierByID[u.TierID]
		if tier == nil && strings.TrimSpace(u.TierID) != "" {
			log.Printf("user %s tier_id %q missing from tier list; limits unavailable", u.ID, u.TierID)
		} else if strings.TrimSpace(u.TierID) == "" {
			log.Printf("user %s has empty tier_id; limits unavailable", u.ID)
		}
		out = append(out, toUserResponseWithTier(u, tier))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (h *Handler) adminGetUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := h.Store.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, toUserResponseWithTier(u, h.loadUserTierForResponse(r.Context(), u)))
}

func (h *Handler) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req UpdateUserRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if currentUser, ok := auth.UserFromContext(r.Context()); ok && currentUser.ID == id {
		if req.Enabled != nil && !*req.Enabled {
			writeError(w, http.StatusConflict, "cannot disable current user")
			return
		}
		if req.Role != nil && *req.Role == store.RoleUser {
			writeError(w, http.StatusConflict, "cannot downgrade current user")
			return
		}
	}
	u, err := h.Store.UpdateUser(r.Context(), id, store.UserUpdates{
		Enabled: req.Enabled, Role: req.Role, TierID: req.TierID, RevokeTokens: req.RevokeTokens,
	})
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if errors.Is(err, store.ErrLastAdmin) {
			writeError(w, http.StatusConflict, "cannot remove last enabled admin")
			return
		}
		if errors.Is(err, store.ErrTierNotAssignable) {
			writeError(w, http.StatusBadRequest, "tier_id must reference an existing tier")
			return
		}
		log.Printf("admin update user %s failed: %v", id, err)
		writeError(w, http.StatusBadRequest, "failed to update user")
		return
	}
	h.invalidateAuthCache()
	writeJSON(w, http.StatusOK, toUserResponseWithTier(u, h.loadUserTierForResponse(r.Context(), u)))
}

func (h *Handler) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	currentUser, ok := auth.UserFromContext(r.Context())
	if ok && currentUser.ID == id {
		writeError(w, http.StatusConflict, "cannot delete current user")
		return
	}
	if err := h.Store.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if errors.Is(err, store.ErrLastAdmin) {
			writeError(w, http.StatusConflict, "cannot remove last enabled admin")
			return
		}
		log.Printf("admin delete user %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}
	h.invalidateAuthCache()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) adminUserUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.Store.GetUserByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	since, ok := parseSince(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid 'since' query parameter; expected RFC3339")
		return
	}
	stats, err := h.Store.GetUserUsageStats(r.Context(), id, since)
	if err != nil {
		log.Printf("admin usage stats for user %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	writeJSON(w, http.StatusOK, toUsageStatsResponse(stats))
}

func (h *Handler) adminListTiers(w http.ResponseWriter, r *http.Request) {
	tiers, err := h.Store.ListTiers(r.Context())
	if err != nil {
		log.Printf("admin list tiers failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load tiers")
		return
	}
	out := make([]TierResponse, 0, len(tiers))
	for _, t := range tiers {
		count, _ := h.Store.CountUsersByTier(r.Context(), t.ID)
		out = append(out, toTierResponse(t, count))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tiers": out})
}

func (h *Handler) adminCreateTier(w http.ResponseWriter, r *http.Request) {
	var req CreateTierRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "tier name is required")
		return
	}
	t, err := h.Store.CreateTier(r.Context(), name, req.Level, req.RPM, req.SuccessLimit)
	if err != nil {
		if errors.Is(err, store.ErrTierNameTaken) {
			writeError(w, http.StatusConflict, "tier name already taken")
			return
		}
		log.Printf("admin create tier failed: %v", err)
		writeError(w, http.StatusBadRequest, "failed to create tier")
		return
	}
	h.invalidateAuthCache()
	writeJSON(w, http.StatusCreated, toTierResponse(t, 0))
}

func (h *Handler) adminUpdateTier(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req UpdateTierRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	t, err := h.Store.UpdateTier(r.Context(), id, store.TierUpdates{
		Name: req.Name, Level: req.Level,
		RPM: req.RPM, SuccessLimit: req.SuccessLimit,
	})
	if err != nil {
		if errors.Is(err, store.ErrTierNotFound) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		if errors.Is(err, store.ErrTierNameTaken) {
			writeError(w, http.StatusConflict, "tier name already taken")
			return
		}
		log.Printf("admin update tier %s failed: %v", id, err)
		writeError(w, http.StatusBadRequest, "failed to update tier")
		return
	}
	count, _ := h.Store.CountUsersByTier(r.Context(), t.ID)
	h.invalidateAuthCache()
	writeJSON(w, http.StatusOK, toTierResponse(t, count))
}

func (h *Handler) adminDeleteTier(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Store.DeleteTier(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrTierNotFound) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		if errors.Is(err, store.ErrTierInUse) {
			writeError(w, http.StatusConflict, "tier is in use; reassign users first")
			return
		}
		log.Printf("admin delete tier %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to delete tier")
		return
	}
	h.invalidateAuthCache()
	w.WriteHeader(http.StatusNoContent)
}
