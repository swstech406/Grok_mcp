package panel

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the password hashing work factor used for panel accounts.
const bcryptCost = 12

func (handler *Handler) registrationChallenge(writer http.ResponseWriter, request *http.Request) {
	registrationMode, err := handler.currentRegistrationMode(request)
	if err != nil {
		log.Printf("load registration mode before issuing challenge failed error_type=%T", err)
		writeError(writer, http.StatusInternalServerError, "registration failed")
		return
	}
	if registrationMode == store.RegistrationModeDisabled {
		writeError(writer, http.StatusForbidden, "registration is disabled")
		return
	}

	authProtector := handler.authProtector()
	challenge, err := authProtector.registrationProof.issue(authProtector.now())
	if err != nil {
		log.Printf("issue registration challenge failed error_type=%T", err)
		writeError(writer, http.StatusInternalServerError, "registration failed")
		return
	}
	writeJSON(writer, http.StatusOK, challenge)
}

func (handler *Handler) register(writer http.ResponseWriter, request *http.Request) {
	var registerRequest RegisterRequest
	if !decodeJSONBody(writer, request, &registerRequest) {
		return
	}
	registrationMode, err := handler.currentRegistrationMode(request)
	if err != nil {
		log.Printf("load registration mode before register failed error_type=%T", err)
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
	authProtector := handler.authProtector()
	if err := authProtector.registrationProof.verifyAndConsume(
		authProtector.now(),
		registerRequest.Proof,
		username,
		registerRequest.InviteCode,
	); err != nil {
		if errors.Is(err, errRegistrationProofCapacity) {
			writeError(writer, http.StatusServiceUnavailable, "registration temporarily unavailable")
			return
		}
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if registrationMode == store.RegistrationModeInvite {
		inviteCodeExists, lookupErr := handler.Store.InviteCodeExists(request.Context(), registerRequest.InviteCode)
		if lookupErr != nil {
			log.Printf("check invite code before register failed error_type=%T", lookupErr)
			writeError(writer, http.StatusInternalServerError, "registration failed")
			return
		}
		if !inviteCodeExists {
			writeError(writer, http.StatusBadRequest, "valid invite code is required")
			return
		}
	} else {
		if existingUser, lookupErr := handler.Store.GetUserByUsername(request.Context(), username); lookupErr != nil {
			log.Printf("check user %q before register failed error_type=%T", username, lookupErr)
			writeError(writer, http.StatusInternalServerError, "registration failed")
			return
		} else if existingUser != nil {
			writeError(writer, http.StatusConflict, "username already taken")
			return
		}
	}
	passwordWorkRelease := authProtector.tryAcquirePasswordWork()
	if passwordWorkRelease == nil {
		writeRetryAfter(writer, time.Second)
		writeError(writer, http.StatusServiceUnavailable, "registration temporarily unavailable")
		return
	}
	var passwordHash []byte
	func() {
		defer passwordWorkRelease()
		passwordHash, err = handler.generatePasswordHash(registerRequest.Password)
	}()
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
		log.Printf("register user %q failed error_type=%T", username, err)
		writeError(writer, http.StatusInternalServerError, "registration failed")
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
		log.Printf("load registration settings failed error_type=%T", err)
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
		return store.RegistrationModeDisabled, nil
	}
	return store.NormalizeRegistrationMode(handler.InitialServerSettings.RegistrationMode)
}
