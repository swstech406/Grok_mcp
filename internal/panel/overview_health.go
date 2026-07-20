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

func (handler *Handler) overviewHealth(writer http.ResponseWriter, request *http.Request) {
	status := handler.evaluateOverviewHealth(request)
	checkedAt := time.Now().UTC()
	writeJSON(writer, http.StatusOK, OverviewHealthResponse{
		Status:    status,
		CheckedAt: checkedAt,
	})
}

func (handler *Handler) evaluateOverviewHealth(request *http.Request) OverviewHealthStatus {
	if handler.ModelLister == nil || handler.Store == nil {
		return OverviewHealthUnknown
	}

	serverSettings, _, err := handler.loadEffectiveServerSettings(request)
	if err != nil {
		return OverviewHealthUnknown
	}
	serverSettings, err = config.NormalizeServerSettings(serverSettings)
	if err != nil {
		return OverviewHealthUnknown
	}

	probeContext, cancelProbe := context.WithTimeout(request.Context(), overviewHealthProbeTimeout)
	defer cancelProbe()

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
