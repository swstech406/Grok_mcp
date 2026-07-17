package panel

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost 为密码哈希工作因子；12 为当前常见基线（DefaultCost=10 在 GPU 离线破解下偏快）。
const bcryptCost = 12

// loadUserTierForResponse 解析用户所属 tier；失败时打日志并返回 nil，
// 由 toUserResponseWithTier 标记 limits_unavailable，避免把零值限额显示成不限。
func (handler *Handler) loadUserTierForResponse(ctx context.Context, user *store.User) *store.Tier {
	if user == nil {
		return nil
	}
	tierID := strings.TrimSpace(user.TierID)
	if tierID == "" {
		log.Printf("user %s has empty tier_id; limits unavailable", user.ID)
		return nil
	}
	tier, err := handler.Store.GetTierByID(ctx, tierID)
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

func (handler *Handler) register(writer http.ResponseWriter, request *http.Request) {
	var registerRequest RegisterRequest
	if !decodeJSONBody(writer, request, &registerRequest) {
		return
	}
	registrationMode, err := handler.currentRegistrationMode(request)
	if err != nil {
		log.Printf("load registration mode before register failed: %v", err)
		writeError(writer, http.StatusInternalServerError, "registration failed")
		return
	}
	if registrationMode == store.RegistrationModeDisabled {
		writeError(writer, http.StatusForbidden, "registration is disabled")
		return
	}
	username, err := validatePanelAuthCredentials(registerRequest.Username, registerRequest.Password)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if registrationMode == store.RegistrationModeInvite {
		inviteCodeExists, lookupErr := handler.Store.InviteCodeExists(request.Context(), registerRequest.InviteCode)
		if lookupErr != nil {
			log.Printf("check invite code before register failed: %v", lookupErr)
			writeError(writer, http.StatusInternalServerError, "registration failed")
			return
		}
		if !inviteCodeExists {
			writeError(writer, http.StatusBadRequest, "valid invite code is required")
			return
		}
	} else {
		if existingUser, lookupErr := handler.Store.GetUserByUsername(request.Context(), username); lookupErr != nil {
			log.Printf("check user %q before register failed: %v", username, lookupErr)
			writeError(writer, http.StatusInternalServerError, "registration failed")
			return
		} else if existingUser != nil {
			writeError(writer, http.StatusConflict, "username already taken")
			return
		}
	}
	passwordHash, err := handler.generatePasswordHash(registerRequest.Password)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "password hash failed")
		return
	}
	// The mode read above is only an inexpensive precheck. The store selects
	// and enforces the authoritative mode in the same transaction as creation,
	// so a mode change during password hashing cannot bypass the new policy.
	user, err := handler.Store.RegisterUserWithCurrentMode(
		request.Context(),
		username,
		string(passwordHash),
		registerRequest.InviteCode,
		registrationMode,
	)
	if err != nil {
		if errors.Is(err, store.ErrRegistrationDisabled) {
			writeError(writer, http.StatusForbidden, "registration is disabled")
			return
		}
		if errors.Is(err, store.ErrUsernameTaken) {
			writeError(writer, http.StatusConflict, "username already taken")
			return
		}
		if errors.Is(err, store.ErrInviteCodeInvalid) {
			writeError(writer, http.StatusBadRequest, "valid invite code is required")
			return
		}
		if errors.Is(err, store.ErrInviteCodeDisabled) {
			writeError(writer, http.StatusForbidden, "invite code is disabled")
			return
		}
		if errors.Is(err, store.ErrInviteCodeExhausted) {
			writeError(writer, http.StatusForbidden, "invite code registration limit reached")
			return
		}
		log.Printf("register user %q failed: %v", username, err)
		writeError(writer, http.StatusBadRequest, "registration failed")
		return
	}
	writeJSON(writer, http.StatusCreated, toUserResponseWithTier(user, handler.loadUserTierForResponse(request.Context(), user)))
}

func (handler *Handler) generatePasswordHash(password string) ([]byte, error) {
	if handler.passwordHashGenerator != nil {
		return handler.passwordHashGenerator([]byte(password), bcryptCost)
	}
	return bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
}

func (handler *Handler) registrationSettings(writer http.ResponseWriter, request *http.Request) {
	registrationMode, err := handler.currentRegistrationMode(request)
	if err != nil {
		log.Printf("load registration settings failed: %v", err)
		writeError(writer, http.StatusInternalServerError, "failed to load registration settings")
		return
	}
	writeJSON(writer, http.StatusOK, RegistrationSettingsResponse{RegistrationMode: registrationMode})
}

func (handler *Handler) currentRegistrationMode(request *http.Request) (store.RegistrationMode, error) {
	storedSettings, err := handler.Store.GetServerSettings(request.Context())
	if err != nil {
		return "", err
	}
	if storedSettings != nil {
		return store.NormalizeRegistrationMode(storedSettings.RegistrationMode)
	}
	if handler.InitialServerSettings.RegistrationMode == "" {
		return store.RegistrationModeFree, nil
	}
	return store.NormalizeRegistrationMode(handler.InitialServerSettings.RegistrationMode)
}

// dummyBcryptHash 是用于拉平登录时序的固定 bcrypt 哈希。
// 它在包初始化时用 bcryptCost 生成（与 register 一致的成本因子），
// 保证 CompareHashAndPassword 会执行完整的密钥派生流程，耗时可与真实用户哈希相当。
// 其明文密码无关紧要——仅用于在用户不存在时消耗相近的 CPU 时间。
var dummyBcryptHash = func() []byte {
	dummyHash, err := bcrypt.GenerateFromPassword([]byte("grok-mcp-timing-dummy-password"), bcryptCost)
	if err != nil {
		// 理论上不会失败；兜底返回一个空切片，此时退化为原始的快速失败行为。
		return nil
	}
	return dummyHash
}()

func (handler *Handler) login(writer http.ResponseWriter, request *http.Request) {
	var loginRequest LoginRequest
	if !decodeJSONBody(writer, request, &loginRequest) {
		return
	}
	username, err := validatePanelAuthCredentials(loginRequest.Username, loginRequest.Password)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	authProtector := handler.authProtector()
	clientIP, shouldApplyIPProtection, clientIPError := authProtector.clientIPForProtection(request)
	if clientIPError != nil {
		writeError(writer, http.StatusBadRequest, ratelimit.ErrInvalidForwardedClientIPHeaders.Error())
		return
	}
	if shouldApplyIPProtection {
		if locked, retryAfter := authProtector.LoginLocked(username, clientIP); locked {
			writeRetryAfter(writer, retryAfter)
			writeError(writer, http.StatusTooManyRequests, "too many failed login attempts")
			return
		}
	}
	user, err := handler.Store.GetUserByUsername(request.Context(), username)
	// 用户不存在时也执行一次 dummy bcrypt 比较，以拉平响应时间，避免通过时序差异枚举有效用户名。
	hashToCheck := dummyBcryptHash
	if err == nil && user != nil {
		hashToCheck = []byte(user.PasswordHash)
	}
	compareErr := bcrypt.CompareHashAndPassword(hashToCheck, []byte(loginRequest.Password))
	if err != nil || user == nil {
		// 用户不存在：上面已执行 dummy 比较，这里统一返回未授权，时序与存在用户分支一致。
		if shouldApplyIPProtection {
			authProtector.RecordLoginFailure(username, clientIP)
		}
		writeError(writer, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if compareErr != nil {
		if shouldApplyIPProtection {
			authProtector.RecordLoginFailure(username, clientIP)
		}
		writeError(writer, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !user.Enabled {
		if shouldApplyIPProtection {
			authProtector.RecordLoginFailure(username, clientIP)
		}
		writeError(writer, http.StatusForbidden, "user disabled")
		return
	}
	if shouldApplyIPProtection {
		authProtector.RecordLoginSuccess(username, clientIP)
	}
	token, expiresAt, err := auth.IssuePanelToken(handler.JWTSecret, user, 0)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "token issue failed")
		return
	}
	writeJSON(writer, http.StatusOK, LoginResponse{
		Token: token, ExpiresAt: expiresAt, User: toUserResponseWithTier(user, handler.loadUserTierForResponse(request.Context(), user)),
	})
}

func (handler *Handler) me(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	freshUser, err := handler.Store.GetUserByID(request.Context(), user.ID)
	if err != nil {
		log.Printf("get user %s failed: %v", user.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to load user")
		return
	}
	writeJSON(writer, http.StatusOK, toUserResponseWithTier(freshUser, handler.loadUserTierForResponse(request.Context(), freshUser)))
}
