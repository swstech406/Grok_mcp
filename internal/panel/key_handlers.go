package panel

import (
	"log"
	"net/http"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

func (handler *Handler) listKeys(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit, err := parsePanelPageLimit(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	cursor, err := parseTimeIDCursor(request, cursorKindKeys)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	page, err := handler.Store.ListKeysByUserPage(request.Context(), user.ID, cursor, limit)
	if err != nil {
		log.Printf("list keys for user %s failed: %v", user.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to load keys")
		return
	}
	response := KeysResponse{
		Keys:        make([]KeyResponse, 0, len(page.Keys)),
		NextCursor:  encodeTimeIDCursor(cursorKindKeys, page.NextCursor),
		HasMore:     page.HasMore,
		TotalCount:  page.TotalCount,
		ActiveCount: page.ActiveCount,
	}
	for _, apiKey := range page.Keys {
		response.Keys = append(response.Keys, toKeyResponse(apiKey))
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) createKey(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	var createRequest CreateKeyRequest
	if !decodeJSONBody(writer, request, &createRequest) {
		return
	}
	if strings.TrimSpace(createRequest.Name) == "" {
		writeError(writer, http.StatusBadRequest, "name is required")
		return
	}
	apiKey, rawAPIKey, err := handler.Store.CreateKey(request.Context(), user.ID, createRequest.Name)
	if err != nil {
		log.Printf("create key for user %s failed: %v", user.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to create key")
		return
	}
	writeJSON(writer, http.StatusCreated, CreateKeyResponse{Key: toKeyResponse(apiKey), APIKey: rawAPIKey})
}

func (handler *Handler) revealKey(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	keyID := request.PathValue("id")
	apiKey, err := handler.Store.GetKeyByID(request.Context(), keyID)
	if err != nil || apiKey.UserID != user.ID {
		writeError(writer, http.StatusNotFound, "api key not found")
		return
	}
	rawAPIKey, err := handler.Store.RevealKey(request.Context(), keyID)
	if err != nil {
		log.Printf("reveal key %s failed: %v", keyID, err)
		writeError(writer, http.StatusInternalServerError, "failed to reveal key")
		return
	}
	writeJSON(writer, http.StatusOK, RevealKeyResponse{APIKey: rawAPIKey})
}

func (handler *Handler) updateKey(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	keyID := request.PathValue("id")
	apiKey, err := handler.Store.GetKeyByID(request.Context(), keyID)
	if err != nil || apiKey.UserID != user.ID {
		writeError(writer, http.StatusNotFound, "api key not found")
		return
	}
	var updateRequest UpdateKeyRequest
	if !decodeJSONBody(writer, request, &updateRequest) {
		return
	}
	if updateRequest.Name != nil && strings.TrimSpace(*updateRequest.Name) == "" {
		writeError(writer, http.StatusBadRequest, "name must not be empty")
		return
	}
	updatedKey, err := handler.Store.UpdateKey(request.Context(), keyID, store.KeyUpdates{
		Name:    updateRequest.Name,
		Enabled: updateRequest.Enabled,
	})
	if err != nil {
		log.Printf("update key %s failed: %v", keyID, err)
		writeError(writer, http.StatusBadRequest, "failed to update key")
		return
	}
	handler.invalidateAuthCache()
	writeJSON(writer, http.StatusOK, toKeyResponse(updatedKey))
}

func (handler *Handler) deleteKey(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	keyID := request.PathValue("id")
	apiKey, err := handler.Store.GetKeyByID(request.Context(), keyID)
	if err != nil || apiKey.UserID != user.ID {
		writeError(writer, http.StatusNotFound, "api key not found")
		return
	}
	if err := handler.Store.DeleteKey(request.Context(), keyID); err != nil {
		log.Printf("delete key %s failed: %v", keyID, err)
		writeError(writer, http.StatusInternalServerError, "failed to delete key")
		return
	}
	handler.invalidateAuthCache()
	writer.WriteHeader(http.StatusNoContent)
}
