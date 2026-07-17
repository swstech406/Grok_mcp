package panel

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

func (handler *Handler) adminListTiers(writer http.ResponseWriter, request *http.Request) {
	limit, err := parsePanelPageLimit(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	cursor, err := parseTierCursor(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	page, err := handler.Store.ListTiersPage(request.Context(), cursor, limit)
	if err != nil {
		log.Printf("admin list tiers failed: %v", err)
		writeError(writer, http.StatusInternalServerError, "failed to load tiers")
		return
	}
	response := TiersResponse{
		Tiers:      make([]TierResponse, 0, len(page.Tiers)),
		NextCursor: encodeTierCursor(page.NextCursor),
		HasMore:    page.HasMore,
		TotalCount: page.TotalCount,
	}
	for _, tier := range page.Tiers {
		userCount, _ := handler.Store.CountUsersByTier(request.Context(), tier.ID)
		response.Tiers = append(response.Tiers, toTierResponse(tier, userCount))
	}
	response.AssignedUserCount, _ = handler.Store.CountUsers(request.Context())
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) adminCreateTier(writer http.ResponseWriter, request *http.Request) {
	var createRequest CreateTierRequest
	if !decodeJSONBody(writer, request, &createRequest) {
		return
	}
	name := strings.TrimSpace(createRequest.Name)
	if name == "" {
		writeError(writer, http.StatusBadRequest, "tier name is required")
		return
	}
	tier, err := handler.Store.CreateTier(request.Context(), name, createRequest.Level, createRequest.RPM, createRequest.SuccessLimit)
	if err != nil {
		if errors.Is(err, store.ErrTierNameTaken) {
			writeError(writer, http.StatusConflict, "tier name already taken")
			return
		}
		log.Printf("admin create tier failed: %v", err)
		writeError(writer, http.StatusBadRequest, "failed to create tier")
		return
	}
	handler.invalidateAuthCache()
	writeJSON(writer, http.StatusCreated, toTierResponse(tier, 0))
}

func (handler *Handler) adminUpdateTier(writer http.ResponseWriter, request *http.Request) {
	tierID := request.PathValue("id")
	var updateRequest UpdateTierRequest
	if !decodeJSONBody(writer, request, &updateRequest) {
		return
	}
	tier, err := handler.Store.UpdateTier(request.Context(), tierID, store.TierUpdates{
		Name:         updateRequest.Name,
		Level:        updateRequest.Level,
		RPM:          updateRequest.RPM,
		SuccessLimit: updateRequest.SuccessLimit,
	})
	if err != nil {
		if errors.Is(err, store.ErrTierNotFound) {
			writeError(writer, http.StatusNotFound, "tier not found")
			return
		}
		if errors.Is(err, store.ErrTierNameTaken) {
			writeError(writer, http.StatusConflict, "tier name already taken")
			return
		}
		log.Printf("admin update tier %s failed: %v", tierID, err)
		writeError(writer, http.StatusBadRequest, "failed to update tier")
		return
	}
	userCount, _ := handler.Store.CountUsersByTier(request.Context(), tier.ID)
	handler.invalidateAuthCache()
	writeJSON(writer, http.StatusOK, toTierResponse(tier, userCount))
}

func (handler *Handler) adminDeleteTier(writer http.ResponseWriter, request *http.Request) {
	tierID := request.PathValue("id")
	if err := handler.Store.DeleteTier(request.Context(), tierID); err != nil {
		if errors.Is(err, store.ErrTierNotFound) {
			writeError(writer, http.StatusNotFound, "tier not found")
			return
		}
		if errors.Is(err, store.ErrTierInUse) {
			writeError(writer, http.StatusConflict, "tier is in use; reassign users first")
			return
		}
		log.Printf("admin delete tier %s failed: %v", tierID, err)
		writeError(writer, http.StatusInternalServerError, "failed to delete tier")
		return
	}
	handler.invalidateAuthCache()
	writer.WriteHeader(http.StatusNoContent)
}
