package panel

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

const settingsSavedNotAppliedCode = "settings_saved_not_applied"

type effectiveServerSettings struct {
	Runtime   config.ServerSettings
	Revision  int64
	UpdatedAt *time.Time
}

type savedNotAppliedErrorResponse struct {
	Code             string `json:"code"`
	Error            string `json:"error"`
	PersistedVersion int64  `json:"persisted_version"`
	LiveVersion      int64  `json:"live_version"`
}

func (h *Handler) adminGetServerSettings(w http.ResponseWriter, r *http.Request) {
	effectiveSettings, err := h.loadEffectiveServerSettings(r)
	if err != nil {
		log.Printf("admin get server settings failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load server settings")
		return
	}
	liveVersion := h.currentLiveServerSettingsVersion(effectiveSettings.Revision)
	writeJSON(w, http.StatusOK, toServerSettingsResponse(
		effectiveSettings.Runtime,
		effectiveSettings.UpdatedAt,
		effectiveSettings.Revision,
		liveVersion,
	))
}

func (h *Handler) adminUpdateServerSettings(w http.ResponseWriter, r *http.Request) {
	var req UpdateServerSettingsRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	h.settingsUpdateMutex.Lock()
	defer h.settingsUpdateMutex.Unlock()

	currentSettings, err := h.loadEffectiveServerSettings(r)
	if err != nil {
		log.Printf("admin load current server settings failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load server settings")
		return
	}

	updatedSettings := mergeServerSettingsRequest(currentSettings.Runtime, req)
	normalizedSettings, err := config.NormalizeServerSettings(updatedSettings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	storedSettings, err := h.Store.UpsertServerSettings(r.Context(), store.ServerSettings{Runtime: normalizedSettings})
	if err != nil {
		log.Printf("admin persist server settings failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to save server settings")
		return
	}
	h.invalidateOverviewHealthCache()

	if h.SettingsApplier != nil {
		if err := h.SettingsApplier.ApplyServerSettings(normalizedSettings, storedSettings.Revision); err != nil {
			log.Printf("admin apply server settings failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, savedNotAppliedErrorResponse{
				Code:             settingsSavedNotAppliedCode,
				Error:            "settings were saved but are not active",
				PersistedVersion: storedSettings.Revision,
				LiveVersion:      h.SettingsApplier.LiveServerSettingsVersion(),
			})
			return
		}
		// A health request may cache the intentional version mismatch while the
		// apply call is in progress. Clear that transient result after convergence.
		h.invalidateOverviewHealthCache()
	}

	updatedAt := storedSettings.UpdatedAt
	liveVersion := h.currentLiveServerSettingsVersion(storedSettings.Revision)
	writeJSON(w, http.StatusOK, toServerSettingsResponse(
		storedSettings.Runtime,
		&updatedAt,
		storedSettings.Revision,
		liveVersion,
	))
}

func (h *Handler) adminListModels(w http.ResponseWriter, r *http.Request) {
	if h.ModelLister == nil {
		writeError(w, http.StatusServiceUnavailable, "model listing is not configured")
		return
	}

	models, err := h.ModelLister.ListModels(r.Context())
	if err != nil {
		log.Printf("admin list models failed: %v", err)
		writeError(w, http.StatusBadGateway, "failed to list upstream models")
		return
	}

	// Reapply the Grok-only filter at the HTTP boundary even though the upstream
	// client already filters, so non-Grok models can never be exposed downstream.
	filteredModels := grok.FilterGrokModels(models)
	writeJSON(w, http.StatusOK, toModelsResponse(filteredModels))
}

func (h *Handler) loadEffectiveServerSettings(r *http.Request) (effectiveServerSettings, error) {
	return h.loadEffectiveServerSettingsContext(r.Context())
}

func (h *Handler) loadEffectiveServerSettingsContext(ctx context.Context) (effectiveServerSettings, error) {
	storedSettings, err := h.Store.GetServerSettings(ctx)
	if err != nil {
		return effectiveServerSettings{}, err
	}
	if storedSettings != nil {
		updatedAt := storedSettings.UpdatedAt
		return effectiveServerSettings{
			Runtime:   storedSettings.Runtime,
			Revision:  storedSettings.Revision,
			UpdatedAt: &updatedAt,
		}, nil
	}
	if h.InitialServerSettings == (config.ServerSettings{}) {
		return effectiveServerSettings{}, nil
	}
	settings, err := config.NormalizeServerSettings(h.InitialServerSettings)
	if err != nil {
		return effectiveServerSettings{}, err
	}
	return effectiveServerSettings{Runtime: settings}, nil
}

func (h *Handler) currentLiveServerSettingsVersion(persistedVersion int64) int64 {
	if h.SettingsApplier == nil {
		return persistedVersion
	}
	return h.SettingsApplier.LiveServerSettingsVersion()
}

func mergeServerSettingsRequest(currentSettings config.ServerSettings, req UpdateServerSettingsRequest) config.ServerSettings {
	mergedSettings := currentSettings
	if req.CPABaseURL != nil {
		mergedSettings.CPABaseURL = *req.CPABaseURL
	}
	if req.CPAAPIKey != nil && strings.TrimSpace(*req.CPAAPIKey) != "" {
		mergedSettings.CPAAPIKey = *req.CPAAPIKey
	}
	if req.UpstreamProtocol != nil {
		mergedSettings.UpstreamProtocol = *req.UpstreamProtocol
	}
	if req.Model != nil {
		mergedSettings.Model = *req.Model
	}
	if req.TimeoutSeconds != nil {
		mergedSettings.TimeoutSeconds = *req.TimeoutSeconds
	}
	if req.MCPGlobalSearchConcurrency != nil {
		mergedSettings.MCPGlobalSearchConcurrency = *req.MCPGlobalSearchConcurrency
	}
	if req.MCPUserSearchConcurrency != nil {
		mergedSettings.MCPUserSearchConcurrency = *req.MCPUserSearchConcurrency
	}
	if req.ProxyURL != nil {
		mergedSettings.ProxyURL = *req.ProxyURL
	}
	if req.ProxyEnabled != nil {
		mergedSettings.ProxyEnabled = *req.ProxyEnabled
	}
	if req.RegistrationMode != nil {
		mergedSettings.RegistrationMode = *req.RegistrationMode
	}
	if req.Debug != nil {
		mergedSettings.Debug = *req.Debug
	}
	if req.OperationsMetricsEnabled != nil {
		mergedSettings.OperationsMetricsEnabled = *req.OperationsMetricsEnabled
	}
	return mergedSettings
}
