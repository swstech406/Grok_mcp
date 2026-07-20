package panel

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
)

const overviewHealthProbeTimeout = 5 * time.Second
const overviewHealthCacheTTL = 30 * time.Second

func (handler *Handler) overviewHealth(writer http.ResponseWriter, request *http.Request) {
	response, completed := handler.loadOverviewHealth(request.Context())
	if !completed {
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) loadOverviewHealth(requestContext context.Context) (OverviewHealthResponse, bool) {
	for {
		now := time.Now()
		handler.overviewHealthState.mutex.Lock()
		if !handler.overviewHealthState.cachedResponse.CheckedAt.IsZero() && now.Before(handler.overviewHealthState.cacheExpiresAt) {
			cachedResponse := handler.overviewHealthState.cachedResponse
			handler.overviewHealthState.mutex.Unlock()
			return cachedResponse, true
		}

		probe := handler.overviewHealthState.inFlightProbe
		if probe == nil {
			probe = &overviewHealthProbe{
				generation: handler.overviewHealthState.generation,
				done:       make(chan struct{}),
			}
			handler.overviewHealthState.inFlightProbe = probe
			go handler.runOverviewHealthProbe(probe)
		}
		handler.overviewHealthState.mutex.Unlock()

		select {
		case <-probe.done:
			// An invalidation can finish an older probe without populating the
			// current cache. Loop so this request joins or starts the new probe.
			continue
		case <-requestContext.Done():
			return OverviewHealthResponse{}, false
		}
	}
}

func (handler *Handler) runOverviewHealthProbe(probe *overviewHealthProbe) {
	probeContext, cancelProbe := context.WithTimeout(context.Background(), overviewHealthProbeTimeout)
	defer cancelProbe()

	response := OverviewHealthResponse{
		Status:    handler.evaluateOverviewHealth(probeContext),
		CheckedAt: time.Now().UTC(),
	}

	handler.overviewHealthState.mutex.Lock()
	if handler.overviewHealthState.inFlightProbe == probe {
		handler.overviewHealthState.inFlightProbe = nil
	}
	if handler.overviewHealthState.generation == probe.generation {
		handler.overviewHealthState.cachedResponse = response
		handler.overviewHealthState.cacheExpiresAt = response.CheckedAt.Add(overviewHealthCacheTTL)
	}
	close(probe.done)
	handler.overviewHealthState.mutex.Unlock()
}

func (handler *Handler) invalidateOverviewHealthCache() {
	handler.overviewHealthState.mutex.Lock()
	handler.overviewHealthState.generation++
	handler.overviewHealthState.cachedResponse = OverviewHealthResponse{}
	handler.overviewHealthState.cacheExpiresAt = time.Time{}
	handler.overviewHealthState.inFlightProbe = nil
	handler.overviewHealthState.mutex.Unlock()
}

func (handler *Handler) evaluateOverviewHealth(probeContext context.Context) OverviewHealthStatus {
	if handler.ModelLister == nil || handler.Store == nil {
		return OverviewHealthUnknown
	}

	effectiveSettings, err := handler.loadEffectiveServerSettingsContext(probeContext)
	if err != nil {
		return OverviewHealthUnknown
	}
	if effectiveSettings.Revision != handler.currentLiveServerSettingsVersion(effectiveSettings.Revision) {
		return OverviewHealthUnknown
	}
	serverSettings, err := config.NormalizeServerSettings(effectiveSettings.Runtime)
	if err != nil {
		return OverviewHealthUnknown
	}

	models, err := handler.ModelLister.ListModels(probeContext)
	if err != nil {
		return OverviewHealthUnhealthy
	}

	configuredModel := strings.TrimSpace(serverSettings.Model)
	for _, model := range grok.FilterGrokModels(models) {
		if strings.EqualFold(strings.TrimSpace(model.ID), configuredModel) {
			return OverviewHealthHealthy
		}
	}
	return OverviewHealthDegraded
}
