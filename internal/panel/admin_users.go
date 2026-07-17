package panel

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

func (handler *Handler) adminListUsers(writer http.ResponseWriter, request *http.Request) {
	limit, err := parsePanelPageLimit(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	cursor, err := parseTimeIDCursor(request, cursorKindUsers)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	page, err := handler.Store.ListUsersPage(request.Context(), cursor, limit)
	if err != nil {
		log.Printf("admin list users failed: %v", err)
		writeError(writer, http.StatusInternalServerError, "failed to load users")
		return
	}
	tierIDs := make([]string, 0, len(page.Users))
	seenTierIDs := make(map[string]struct{}, len(page.Users))
	for _, user := range page.Users {
		tierID := strings.TrimSpace(user.TierID)
		if tierID == "" {
			continue
		}
		if _, alreadyAdded := seenTierIDs[tierID]; alreadyAdded {
			continue
		}
		seenTierIDs[tierID] = struct{}{}
		tierIDs = append(tierIDs, tierID)
	}
	tierByID, err := handler.Store.GetTiersByIDs(request.Context(), tierIDs)
	if err != nil {
		log.Printf("load tiers while listing users failed: %v", err)
		tierByID = make(map[string]*store.Tier)
	}
	response := UsersResponse{
		Users:      make([]UserResponse, 0, len(page.Users)),
		NextCursor: encodeTimeIDCursor(cursorKindUsers, page.NextCursor),
		HasMore:    page.HasMore,
		TotalCount: page.TotalCount,
	}
	for _, user := range page.Users {
		tierID := strings.TrimSpace(user.TierID)
		tier := tierByID[tierID]
		if tier == nil && tierID != "" {
			log.Printf("user %s tier_id %q missing from tier list; limits unavailable", user.ID, user.TierID)
		} else if tierID == "" {
			log.Printf("user %s has empty tier_id; limits unavailable", user.ID)
		}
		response.Users = append(response.Users, toUserResponseWithTier(user, tier))
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) adminGetUser(writer http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("id")
	user, err := handler.Store.GetUserByID(request.Context(), userID)
	if err != nil {
		writeError(writer, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(writer, http.StatusOK, toUserResponseWithTier(user, handler.loadUserTierForResponse(request.Context(), user)))
}

func (handler *Handler) adminUpdateUser(writer http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("id")
	var updateRequest UpdateUserRequest
	if !decodeJSONBody(writer, request, &updateRequest) {
		return
	}
	if currentUser, ok := auth.UserFromContext(request.Context()); ok && currentUser.ID == userID {
		if updateRequest.Enabled != nil && !*updateRequest.Enabled {
			writeError(writer, http.StatusConflict, "cannot disable current user")
			return
		}
		if updateRequest.Role != nil && *updateRequest.Role == store.RoleUser {
			writeError(writer, http.StatusConflict, "cannot downgrade current user")
			return
		}
	}
	user, err := handler.Store.UpdateUser(request.Context(), userID, store.UserUpdates{
		Enabled:      updateRequest.Enabled,
		Role:         updateRequest.Role,
		TierID:       updateRequest.TierID,
		RevokeTokens: updateRequest.RevokeTokens,
	})
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeError(writer, http.StatusNotFound, "user not found")
			return
		}
		if errors.Is(err, store.ErrLastAdmin) {
			writeError(writer, http.StatusConflict, "cannot remove last enabled admin")
			return
		}
		if errors.Is(err, store.ErrTierNotAssignable) {
			writeError(writer, http.StatusBadRequest, "tier_id must reference an existing tier")
			return
		}
		log.Printf("admin update user %s failed: %v", userID, err)
		writeError(writer, http.StatusBadRequest, "failed to update user")
		return
	}
	handler.invalidateAuthCache()
	writeJSON(writer, http.StatusOK, toUserResponseWithTier(user, handler.loadUserTierForResponse(request.Context(), user)))
}

func (handler *Handler) adminDeleteUser(writer http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("id")
	currentUser, ok := auth.UserFromContext(request.Context())
	if ok && currentUser.ID == userID {
		writeError(writer, http.StatusConflict, "cannot delete current user")
		return
	}
	if err := handler.Store.DeleteUser(request.Context(), userID); err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeError(writer, http.StatusNotFound, "user not found")
			return
		}
		if errors.Is(err, store.ErrLastAdmin) {
			writeError(writer, http.StatusConflict, "cannot remove last enabled admin")
			return
		}
		log.Printf("admin delete user %s failed: %v", userID, err)
		writeError(writer, http.StatusInternalServerError, "failed to delete user")
		return
	}
	handler.invalidateAuthCache()
	writer.WriteHeader(http.StatusNoContent)
}
