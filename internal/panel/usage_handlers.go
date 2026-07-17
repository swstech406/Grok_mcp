package panel

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

// parseSince 解析 ?since=RFC3339 查询参数；raw 非空但格式非法时返回 (zero, false)，
// 调用方应据此返回 400，避免静默回退到全量查询导致 usage_log 全表扫描。
func parseSince(request *http.Request) (time.Time, bool) {
	rawSince := strings.TrimSpace(request.URL.Query().Get("since"))
	if rawSince == "" {
		return time.Time{}, true
	}
	parsedSince, err := time.Parse(time.RFC3339, rawSince)
	if err == nil {
		return parsedSince.UTC(), true
	}
	return time.Time{}, false
}

func (handler *Handler) keyUsage(writer http.ResponseWriter, request *http.Request) {
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
	since, ok := parseSince(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid 'since' query parameter; expected RFC3339")
		return
	}
	stats, err := handler.Store.GetUsageStats(request.Context(), keyID, since)
	if err != nil {
		log.Printf("usage stats for key %s failed: %v", keyID, err)
		writeError(writer, http.StatusInternalServerError, "failed to load usage")
		return
	}
	writeJSON(writer, http.StatusOK, toUsageStatsResponse(stats))
}

func (handler *Handler) userUsage(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	since, ok := parseSince(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid 'since' query parameter; expected RFC3339")
		return
	}
	limit, err := parsePanelPageLimit(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	cursor, err := parseUsageRecordCursor(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := handler.Store.GetUserUsageStatsPage(request.Context(), user.ID, since, cursor, limit)
	if err != nil {
		log.Printf("usage stats for user %s failed: %v", user.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to load usage")
		return
	}
	writeJSON(writer, http.StatusOK, toUsageStatsResponse(stats))
}

func (handler *Handler) userUsageRecords(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	since, ok := parseSince(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid 'since' query parameter; expected RFC3339")
		return
	}
	limit, err := parsePanelPageLimit(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	cursor, err := parseUsageRecordCursor(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	page, err := handler.Store.ListUsageRecordsPage(request.Context(), store.UsageRecordListScope{UserID: user.ID}, since, cursor, limit)
	if err != nil {
		log.Printf("usage record page for user %s failed: %v", user.ID, err)
		writeError(writer, http.StatusInternalServerError, "failed to load usage records")
		return
	}
	writeJSON(writer, http.StatusOK, toUsageRecordsResponse(page))
}

func (handler *Handler) usageRecordDetail(writer http.ResponseWriter, request *http.Request) {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}

	usageRecordID, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || usageRecordID <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid usage record id")
		return
	}

	record, err := handler.Store.GetUsageRecordDetail(request.Context(), usageRecordID, store.UsageRecordScope{
		UserID:          user.ID,
		IncludeAllUsers: user.Role == store.RoleAdmin,
	})
	if errors.Is(err, store.ErrUsageRecordNotFound) {
		writeError(writer, http.StatusNotFound, "usage record not found")
		return
	}
	if err != nil {
		log.Printf("usage record %d detail failed: %v", usageRecordID, err)
		writeError(writer, http.StatusInternalServerError, "failed to load usage record")
		return
	}
	writeJSON(writer, http.StatusOK, toUsageRecordDetailResponse(record))
}

func (handler *Handler) adminUserUsage(writer http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("id")
	if _, err := handler.Store.GetUserByID(request.Context(), userID); err != nil {
		writeError(writer, http.StatusNotFound, "user not found")
		return
	}
	since, ok := parseSince(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid 'since' query parameter; expected RFC3339")
		return
	}
	limit, err := parsePanelPageLimit(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	cursor, err := parseUsageRecordCursor(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := handler.Store.GetUserUsageStatsPage(request.Context(), userID, since, cursor, limit)
	if err != nil {
		log.Printf("admin usage stats for user %s failed: %v", userID, err)
		writeError(writer, http.StatusInternalServerError, "failed to load usage")
		return
	}
	writeJSON(writer, http.StatusOK, toUsageStatsResponse(stats))
}
