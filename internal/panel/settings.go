package panel

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/grok"
	"github.com/grok-mcp/internal/store"
)

// ModelLister fetches the currently available upstream Grok models.
type ModelLister interface {
	ListModels(context.Context) ([]grok.Model, error)
}

func (h *Handler) adminGetServerSettings(w http.ResponseWriter, r *http.Request) {
	settings, updatedAt, err := h.loadEffectiveServerSettings(r)
	if err != nil {
		log.Printf("admin get server settings failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load server settings")
		return
	}
	writeJSON(w, http.StatusOK, toServerSettingsResponse(settings, updatedAt))
}

func (h *Handler) adminUpdateServerSettings(w http.ResponseWriter, r *http.Request) {
	currentSettings, _, err := h.loadEffectiveServerSettings(r)
	if err != nil {
		log.Printf("admin load current server settings failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load server settings")
		return
	}

	var req UpdateServerSettingsRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	updatedSettings := mergeServerSettingsRequest(currentSettings, req)
	normalizedSettings, err := config.NormalizeServerSettings(updatedSettings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	storedSettings, err := h.Store.UpsertServerSettings(r.Context(), store.ServerSettingsFromFields(config.SettingsFieldsFromConfig(normalizedSettings)))
	if err != nil {
		log.Printf("admin persist server settings failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to save server settings")
		return
	}

	if h.SettingsApplier != nil {
		if err := h.SettingsApplier.ApplyServerSettings(normalizedSettings); err != nil {
			log.Printf("admin apply server settings failed: %v", err)
			writeError(w, http.StatusInternalServerError, "settings saved but failed to apply")
			return
		}
	}

	updatedAt := storedSettings.UpdatedAt
	writeJSON(w, http.StatusOK, toServerSettingsResponse(normalizedSettings, &updatedAt))
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

func (h *Handler) loadEffectiveServerSettings(r *http.Request) (config.ServerSettings, *time.Time, error) {
	storedSettings, err := h.Store.GetServerSettings(r.Context())
	if err != nil {
		return config.ServerSettings{}, nil, err
	}
	if storedSettings != nil {
		updatedAt := storedSettings.UpdatedAt
		return config.ServerSettingsFromFields(store.SettingsFieldsFromStore(storedSettings)), &updatedAt, nil
	}
	if h.InitialServerSettings == (config.ServerSettings{}) {
		return config.ServerSettings{}, nil, nil
	}
	settings, err := config.NormalizeServerSettings(h.InitialServerSettings)
	if err != nil {
		return config.ServerSettings{}, nil, err
	}
	return settings, nil, nil
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
	return mergedSettings
}
