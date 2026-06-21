package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/grok-mcp/internal/store"
)

// Handler 依赖 Store 实现管理端点；路由由 NewMux 注册，外层需套 AdminTokenMiddleware。
type Handler struct {
	Store store.Store
}

// NewMux 注册 /admin/v1 下的密钥与统计路由（Go 1.22+ 方法风格 ServeMux）。
func NewMux(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/v1/keys", h.createKey)
	mux.HandleFunc("GET /admin/v1/keys", h.listKeys)
	mux.HandleFunc("GET /admin/v1/keys/{id}", h.getKey)
	mux.HandleFunc("PATCH /admin/v1/keys/{id}", h.updateKey)
	mux.HandleFunc("DELETE /admin/v1/keys/{id}", h.deleteKey)
	mux.HandleFunc("GET /admin/v1/keys/{id}/usage", h.keyUsage)
	mux.HandleFunc("GET /admin/v1/stats", h.globalStats)
	return mux
}

// parseSince 解析查询参数 since（RFC3339）；无效或缺失表示不限制起始时间。
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

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	k, raw, err := h.Store.CreateKey(r.Context(), req.Name, req.RateLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, CreateKeyResponse{
		Key: toKeyResponse(k), APIKey: raw,
	})
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Store.ListKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]KeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (h *Handler) getKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	k, err := h.Store.GetKeyByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toKeyResponse(k))
}

func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req UpdateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	k, err := h.Store.UpdateKey(r.Context(), id, store.KeyUpdates{
		Name: req.Name, RateLimit: req.RateLimit, Enabled: req.Enabled,
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toKeyResponse(k))
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Store.DeleteKey(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) keyUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.Store.GetKeyByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	since := parseSince(r)
	stats, err := h.Store.GetUsageStats(r.Context(), id, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toUsageStatsResponse(stats))
}

func (h *Handler) globalStats(w http.ResponseWriter, r *http.Request) {
	since := parseSince(r)
	stats, err := h.Store.GetGlobalStats(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toUsageStatsResponse(stats))
}