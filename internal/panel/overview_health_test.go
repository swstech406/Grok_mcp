package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

type overviewHealthSettingsFailureStore struct {
	store.TestStore
}

func (testStore overviewHealthSettingsFailureStore) GetServerSettings(context.Context) (*store.ServerSettings, error) {
	return nil, errors.New("settings database unavailable")
}

func TestOverviewHealthClassifiesUpstreamState(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "overview-health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.ConfigureAPIKeyEncryption("overview-health-secret-at-least-32-bytes"); err != nil {
		t.Fatal(err)
	}

	_, err = sqliteStore.UpsertServerSettings(context.Background(), store.ServerSettings{Runtime: validOverviewHealthSettings()})
	if err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		name           string
		modelLister    ModelLister
		expectedStatus OverviewHealthStatus
	}{
		{
			name: "configured model is available",
			modelLister: staticModelLister{models: []grok.Model{
				{ID: "grok-4.3"},
			}},
			expectedStatus: OverviewHealthHealthy,
		},
		{
			name: "upstream is reachable but configured model is missing",
			modelLister: staticModelLister{models: []grok.Model{
				{ID: "grok-4.2"},
			}},
			expectedStatus: OverviewHealthDegraded,
		},
		{
			name:           "upstream probe fails",
			modelLister:    staticModelLister{err: errors.New("secret upstream failure")},
			expectedStatus: OverviewHealthUnhealthy,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/panel/v1/overview/health", nil)
			handler := &Handler{Store: sqliteStore, ModelLister: testCase.modelLister}

			handler.overviewHealth(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("health status code = %d, want %d", recorder.Code, http.StatusOK)
			}
			if strings.Contains(recorder.Body.String(), "secret upstream failure") {
				t.Fatalf("health response exposed upstream error: %s", recorder.Body.String())
			}

			var response OverviewHealthResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != testCase.expectedStatus {
				t.Fatalf("health status = %q, want %q", response.Status, testCase.expectedStatus)
			}
			if response.CheckedAt.IsZero() {
				t.Fatal("health response omitted checked_at")
			}
		})
	}
}

func TestOverviewHealthReturnsUnknownWithoutReliableProbeInputs(t *testing.T) {
	testCases := []struct {
		name    string
		handler *Handler
	}{
		{
			name:    "model lister is unavailable",
			handler: &Handler{Store: overviewHealthSettingsFailureStore{}},
		},
		{
			name: "settings cannot be loaded",
			handler: &Handler{
				Store:       overviewHealthSettingsFailureStore{},
				ModelLister: staticModelLister{models: []grok.Model{{ID: "grok-4.3"}}},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/panel/v1/overview/health", nil)

			testCase.handler.overviewHealth(recorder, request)

			var response OverviewHealthResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != OverviewHealthUnknown {
				t.Fatalf("health status = %q, want %q", response.Status, OverviewHealthUnknown)
			}
		})
	}
}

func TestOverviewHealthRouteRequiresAuthentication(t *testing.T) {
	testServer, _, _ := panelTestServer(t)
	defer testServer.Close()

	response, err := http.Get(testServer.URL + "/panel/v1/overview/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated health status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}

func validOverviewHealthSettings() config.ServerSettings {
	return config.ServerSettings{
		CPABaseURL:                 "https://cpa.example.test",
		CPAAPIKey:                  "overview-health-api-key",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		TimeoutSeconds:             120,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}
}
