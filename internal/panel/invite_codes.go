package panel

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

func (h *Handler) adminListInviteCodes(w http.ResponseWriter, r *http.Request) {
	limit, err := parsePanelPageLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cursor, err := parseTimeIDCursor(r, cursorKindInvites)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := h.Store.ListInviteCodesPage(r.Context(), cursor, limit)
	if err != nil {
		log.Printf("admin list invite codes failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to list invite codes")
		return
	}

	response := InviteCodesResponse{
		InviteCodes: make([]InviteCodeResponse, 0, len(page.InviteCodes)),
		NextCursor:  encodeTimeIDCursor(cursorKindInvites, page.NextCursor),
		HasMore:     page.HasMore,
		TotalCount:  page.TotalCount,
	}
	for _, inviteCode := range page.InviteCodes {
		response.InviteCodes = append(response.InviteCodes, toInviteCodeResponse(inviteCode))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) adminCreateInviteCode(w http.ResponseWriter, r *http.Request) {
	var req CreateInviteCodeRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.RegistrationLimit <= 0 {
		writeError(w, http.StatusBadRequest, "registration_limit must be positive")
		return
	}

	createdByUserID := ""
	if user, ok := auth.UserFromContext(r.Context()); ok {
		createdByUserID = user.ID
	}

	inviteCode, rawInviteCode, err := h.Store.CreateInviteCode(r.Context(), createdByUserID, req.RegistrationLimit)
	if err != nil {
		log.Printf("admin create invite code failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to create invite code")
		return
	}
	writeJSON(w, http.StatusCreated, CreateInviteCodeResponse{
		InviteCode: toInviteCodeResponse(inviteCode),
		Code:       rawInviteCode,
	})
}

func (h *Handler) adminUpdateInviteCode(w http.ResponseWriter, r *http.Request) {
	inviteCodeID := strings.TrimSpace(r.PathValue("id"))
	if inviteCodeID == "" {
		writeError(w, http.StatusBadRequest, "invite code id is required")
		return
	}

	var req UpdateInviteCodeRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.RegistrationLimit != nil && *req.RegistrationLimit <= 0 {
		writeError(w, http.StatusBadRequest, "registration_limit must be positive")
		return
	}

	inviteCode, err := h.Store.UpdateInviteCode(r.Context(), inviteCodeID, store.InviteCodeUpdates{
		RegistrationLimit: req.RegistrationLimit,
		Enabled:           req.Enabled,
	})
	if err != nil {
		if errors.Is(err, store.ErrInviteCodeNotFound) {
			writeError(w, http.StatusNotFound, "invite code not found")
			return
		}
		if errors.Is(err, store.ErrInviteCodeLimitTooLow) {
			writeError(w, http.StatusBadRequest, "registration_limit cannot be lower than current usage")
			return
		}
		log.Printf("admin update invite code %q failed: %v", inviteCodeID, err)
		writeError(w, http.StatusInternalServerError, "failed to update invite code")
		return
	}
	writeJSON(w, http.StatusOK, toInviteCodeResponse(inviteCode))
}

func (h *Handler) adminDeleteInviteCode(w http.ResponseWriter, r *http.Request) {
	inviteCodeID := strings.TrimSpace(r.PathValue("id"))
	if inviteCodeID == "" {
		writeError(w, http.StatusBadRequest, "invite code id is required")
		return
	}
	if err := h.Store.DeleteInviteCode(r.Context(), inviteCodeID); err != nil {
		if errors.Is(err, store.ErrInviteCodeNotFound) {
			writeError(w, http.StatusNotFound, "invite code not found")
			return
		}
		log.Printf("admin delete invite code %q failed: %v", inviteCodeID, err)
		writeError(w, http.StatusInternalServerError, "failed to delete invite code")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
