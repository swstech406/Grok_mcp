package panel

import (
	"encoding/json"
	"errors"
	"log"
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
	Store  store.Store
	Config *config.Config
}

// NewMux 注册 /panel/v1 路由（外层需套 JWT 中间件）。
// 管理员路由注册到独立子路由器，并显式套用 auth.RequireAdmin，避免依赖路径前缀做鉴权。
// 调用方应通过 NewAdminSubMux 取得已包裹 admin 中间件的子路由器挂到外层 mux 上。
func NewMux(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /panel/v1/auth/register", h.register)
	mux.HandleFunc("POST /panel/v1/auth/login", h.login)
	mux.HandleFunc("GET /panel/v1/me", h.me)

	mux.HandleFunc("GET /panel/v1/keys", h.listKeys)
	mux.HandleFunc("POST /panel/v1/keys", h.createKey)
	mux.HandleFunc("PATCH /panel/v1/keys/{id}", h.updateKey)
	mux.HandleFunc("DELETE /panel/v1/keys/{id}", h.deleteKey)
	mux.HandleFunc("GET /panel/v1/keys/{id}/usage", h.keyUsage)

	h.RegisterAdminRoutes(mux)
	return mux
}

// RegisterAdminRoutes 在给定 mux 上注册管理员路由，并套上 RequireAdmin 中间件。
// 单独导出便于把管理员路由挂到独立的子路由器上；任何新增管理员路由只要走本方法即受保护。
func (h *Handler) RegisterAdminRoutes(mux *http.ServeMux) {
	admin := http.NewServeMux()
	admin.HandleFunc("GET /panel/v1/admin/users", h.adminListUsers)
	admin.HandleFunc("GET /panel/v1/admin/users/{id}", h.adminGetUser)
	admin.HandleFunc("PATCH /panel/v1/admin/users/{id}", h.adminUpdateUser)
	admin.HandleFunc("GET /panel/v1/admin/users/{id}/usage", h.adminUserUsage)
	mux.Handle("/panel/v1/admin/", auth.RequireAdmin()(admin))
}

func parseSince(r *http.Request) time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get("since"))
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

const maxPanelBodyBytes = 1 << 20 // 1 MiB

// MaxBodyMiddleware 限制面板 JSON 请求体大小，防止恶意超大请求耗尽内存。
func MaxBodyMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxPanelBodyBytes)
			next.ServeHTTP(w, r)
		})
	}
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "username required and password must be at least 8 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hash failed")
		return
	}
	user, err := h.Store.RegisterUser(r.Context(), username, string(hash),
		h.Config.DefaultUserRPM, h.Config.DefaultUserTotalLimit, h.Config.DefaultUserSuccessLimit)
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			writeError(w, http.StatusConflict, "username already taken")
			return
		}
		log.Printf("register user %q failed: %v", username, err)
		writeError(w, http.StatusBadRequest, "registration failed")
		return
	}
	writeJSON(w, http.StatusCreated, toUserResponse(user))
}

// dummyBcryptHash 是用于拉平登录时序的固定 bcrypt 哈希。
// 它在包初始化时用 bcrypt.DefaultCost 生成（与 register 一致的成本因子），
// 保证 CompareHashAndPassword 会执行完整的密钥派生流程，耗时可与真实用户哈希相当。
// 其明文密码无关紧要——仅用于在用户不存在时消耗相近的 CPU 时间。
var dummyBcryptHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("grok-mcp-timing-dummy-password"), bcrypt.DefaultCost)
	if err != nil {
		// 理论上不会失败；兜底返回一个空切片，此时退化为原始的快速失败行为。
		return nil
	}
	return h
}()

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	user, err := h.Store.GetUserByUsername(r.Context(), req.Username)
	// 用户不存在时也执行一次 dummy bcrypt 比较，以拉平响应时间，避免通过时序差异枚举有效用户名。
	hashToCheck := dummyBcryptHash
	if err == nil && user != nil {
		hashToCheck = []byte(user.PasswordHash)
	}
	compareErr := bcrypt.CompareHashAndPassword(hashToCheck, []byte(req.Password))
	if err != nil || user == nil {
		// 用户不存在：上面已执行 dummy 比较，这里统一返回未授权，时序与存在用户分支一致。
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !user.Enabled {
		writeError(w, http.StatusForbidden, "user disabled")
		return
	}
	if compareErr != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, exp, err := auth.IssuePanelToken(h.Config.JWTSecret, user, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: token, ExpiresAt: exp, User: toUserResponse(user),
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
	writeJSON(w, http.StatusOK, toUserResponse(fresh))
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	k, raw, err := h.Store.CreateKey(r.Context(), user.ID, req.Name, 0)
	if err != nil {
		log.Printf("create key for user %s failed: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}
	writeJSON(w, http.StatusCreated, CreateKeyResponse{Key: toKeyResponse(k), APIKey: raw})
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
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
	stats, err := h.Store.GetUsageStats(r.Context(), id, parseSince(r))
	if err != nil {
		log.Printf("usage stats for key %s failed: %v", id, err)
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
	out := make([]UserResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResponse(u))
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
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (h *Handler) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	u, err := h.Store.UpdateUser(r.Context(), id, store.UserUpdates{
		Enabled: req.Enabled, Role: req.Role,
		RPM: req.RPM, TotalLimit: req.TotalLimit, SuccessLimit: req.SuccessLimit,
	})
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		log.Printf("admin update user %s failed: %v", id, err)
		writeError(w, http.StatusBadRequest, "failed to update user")
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (h *Handler) adminUserUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.Store.GetUserByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	stats, err := h.Store.GetUserUsageStats(r.Context(), id, parseSince(r))
	if err != nil {
		log.Printf("admin usage stats for user %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	writeJSON(w, http.StatusOK, toUsageStatsResponse(stats))
}