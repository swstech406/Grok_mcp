package panel

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// loadUserTierForResponse resolves the tier used to populate panel limits.
// Missing tiers are logged and represented as unavailable instead of unlimited.
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
		log.Printf("user %s load tier %q failed error_type=%T; limits unavailable", user.ID, tierID, err)
		return nil
	}
	if tier == nil {
		log.Printf("user %s tier_id %q returned nil; limits unavailable", user.ID, tierID)
		return nil
	}
	return tier
}

// dummyBcryptHash equalizes login timing when a username does not exist.
var dummyBcryptHash = func() []byte {
	dummyHash, err := bcrypt.GenerateFromPassword([]byte("grok-mcp-timing-dummy-password"), bcryptCost)
	if err != nil {
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
		writeClientIPResolutionError(writer, clientIPError)
		return
	}
	var loginAttempt *loginAttempt
	if shouldApplyIPProtection {
		var retryAfter time.Duration
		loginAttempt, retryAfter = authProtector.beginLoginAttempt(username, clientIP)
		if loginAttempt == nil {
			writeRetryAfter(writer, retryAfter)
			writeError(writer, http.StatusTooManyRequests, "too many failed login attempts")
			return
		}
		defer loginAttempt.abandon()
	}
	user, err := handler.Store.GetUserByUsername(request.Context(), username)

	// Always execute bcrypt so unknown usernames cannot be enumerated by timing.
	hashToCheck := dummyBcryptHash
	if err == nil && user != nil {
		hashToCheck = []byte(user.PasswordHash)
	}
	passwordWorkRelease := authProtector.tryAcquirePasswordWork()
	if passwordWorkRelease == nil {
		writeRetryAfter(writer, time.Second)
		writeError(writer, http.StatusServiceUnavailable, "authentication temporarily unavailable")
		return
	}
	var compareErr error
	func() {
		defer passwordWorkRelease()
		compareErr = handler.comparePasswordHash(request.Context(), hashToCheck, []byte(loginRequest.Password))
	}()
	if err != nil || user == nil || compareErr != nil {
		loginAttempt.recordFailure()
		writeError(writer, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !user.Enabled {
		loginAttempt.recordFailure()
		writeError(writer, http.StatusForbidden, "user disabled")
		return
	}
	loginAttempt.recordSuccess()
	token, expiresAt, err := auth.IssuePanelToken(handler.JWTSecret, user, 0)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "token issue failed")
		return
	}
	writeJSON(writer, http.StatusOK, LoginResponse{
		Token: token, ExpiresAt: expiresAt, User: toUserResponseWithTier(user, handler.loadUserTierForResponse(request.Context(), user)),
	})
}

func (handler *Handler) comparePasswordHash(requestContext context.Context, hashedPassword, password []byte) error {
	if handler.passwordHashComparator != nil {
		return handler.passwordHashComparator(requestContext, hashedPassword, password)
	}
	return bcrypt.CompareHashAndPassword(hashedPassword, password)
}

func (handler *Handler) me(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	freshUser, err := handler.Store.GetUserByID(request.Context(), user.ID)
	if err != nil {
		log.Printf("get user %s failed error_type=%T", user.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to load user")
		return
	}
	writeJSON(writer, http.StatusOK, toUserResponseWithTier(freshUser, handler.loadUserTierForResponse(request.Context(), freshUser)))
}

func (handler *Handler) changePassword(writer http.ResponseWriter, request *http.Request) {
	authenticatedUser, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	var changeRequest ChangePasswordRequest
	if !decodeJSONBody(writer, request, &changeRequest) {
		return
	}
	if err := validatePanelPassword(changeRequest.CurrentPassword); err != nil {
		writeError(writer, http.StatusBadRequest, "current_password must be 8-72 bytes")
		return
	}
	if err := validatePanelPassword(changeRequest.NewPassword); err != nil {
		writeError(writer, http.StatusBadRequest, "new_password must be 8-72 bytes")
		return
	}

	currentUser, err := handler.Store.GetUserByID(request.Context(), authenticatedUser.ID)
	if err != nil {
		log.Printf("load user %s for password change failed error_type=%T", authenticatedUser.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to change password")
		return
	}
	passwordWorkRelease := handler.authProtector().tryAcquirePasswordWork()
	if passwordWorkRelease == nil {
		writeRetryAfter(writer, time.Second)
		writeError(writer, http.StatusServiceUnavailable, "authentication temporarily unavailable")
		return
	}
	var replacementPasswordHash []byte
	var compareErr error
	func() {
		defer passwordWorkRelease()
		compareErr = handler.comparePasswordHash(
			request.Context(),
			[]byte(currentUser.PasswordHash),
			[]byte(changeRequest.CurrentPassword),
		)
		if compareErr == nil {
			replacementPasswordHash, err = handler.generatePasswordHash(changeRequest.NewPassword)
		}
	}()
	if compareErr != nil {
		writeError(writer, http.StatusBadRequest, "current password is incorrect")
		return
	}
	if err != nil {
		log.Printf("generate replacement password hash for user %s failed error_type=%T", currentUser.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to change password")
		return
	}

	replacementHash := string(replacementPasswordHash)
	updatedUser, err := handler.Store.UpdateUser(request.Context(), currentUser.ID, store.UserUpdates{
		PasswordHash: &replacementHash,
	})
	if err != nil {
		log.Printf("update password for user %s failed error_type=%T", currentUser.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to change password")
		return
	}
	handler.removeBootstrapCredentialAfterPasswordChange(updatedUser)
	handler.writeReplacementSession(writer, request, updatedUser)
}

func (handler *Handler) revokeSessions(writer http.ResponseWriter, request *http.Request) {
	authenticatedUser, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	revokeTokens := true
	updatedUser, err := handler.Store.UpdateUser(request.Context(), authenticatedUser.ID, store.UserUpdates{
		RevokeTokens: &revokeTokens,
	})
	if err != nil {
		log.Printf("revoke sessions for user %s failed error_type=%T", authenticatedUser.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to revoke sessions")
		return
	}
	handler.writeReplacementSession(writer, request, updatedUser)
}

func (handler *Handler) writeReplacementSession(
	writer http.ResponseWriter,
	request *http.Request,
	updatedUser *store.User,
) {
	token, expiresAt, err := auth.IssuePanelToken(handler.JWTSecret, updatedUser, 0)
	if err != nil {
		log.Printf("issue replacement token for user %s failed error_type=%T", updatedUser.ID, err)
		writeError(writer, http.StatusInternalServerError, "token issue failed")
		return
	}
	writeJSON(writer, http.StatusOK, SessionReplacementResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		User: toUserResponseWithTier(
			updatedUser,
			handler.loadUserTierForResponse(request.Context(), updatedUser),
		),
	})
}

func (handler *Handler) removeBootstrapCredentialAfterPasswordChange(updatedUser *store.User) {
	if updatedUser == nil || handler.BootstrapCredentialCleaner == nil {
		return
	}
	bootstrapUsername := strings.TrimSpace(handler.BootstrapAdminUsername)
	if bootstrapUsername == "" {
		bootstrapUsername = "admin"
	}
	if !strings.EqualFold(updatedUser.Username, bootstrapUsername) {
		return
	}
	if err := handler.BootstrapCredentialCleaner(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf(
			"remove bootstrap credential file %s after password change failed error_type=%T",
			handler.BootstrapCredentialsPath,
			err,
		)
	}
}
