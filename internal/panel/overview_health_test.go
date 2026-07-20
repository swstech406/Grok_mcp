package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

type overviewHealthSettingsFailureStore struct {
	store.TestStore
}

type countingOverviewHealthModelLister struct {
	models       []grok.Model
	probeStarted chan struct{}
	releaseProbe chan struct{}
	startOnce    sync.Once
	callCount    atomic.Int64
}

func (testStore overviewHealthSettingsFailureStore) GetServerSettings(context.Context) (*store.ServerSettings, error) {
	return nil, errors.New("settings database unavailable")
}

func (modelLister *countingOverviewHealthModelLister) ListModels(ctx context.Context) ([]grok.Model, error) {
	modelLister.callCount.Add(1)
	if modelLister.probeStarted != nil {
		modelLister.startOnce.Do(func() {
			close(modelLister.probeStarted)
		})
	}
	if modelLister.releaseProbe != nil {
		select {
		case <-modelLister.releaseProbe:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return modelLister.models, nil
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

func TestOverviewHealthSkipsProbeWhenPersistedAndLiveSettingsDiffer(t *testing.T) {
	modelLister := &countingOverviewHealthModelLister{
		models: []grok.Model{{ID: "grok-4.3"}},
	}
	handler := &Handler{
		Store:           newOverviewHealthTestStore(t),
		ModelLister:     modelLister,
		SettingsApplier: &recordingSettingsApplier{liveRevision: 0},
	}

	status := handler.evaluateOverviewHealth(context.Background())

	if status != OverviewHealthUnknown {
		t.Fatalf("health status = %q, want %q", status, OverviewHealthUnknown)
	}
	if probeCount := modelLister.callCount.Load(); probeCount != 0 {
		t.Fatalf("upstream probe count = %d, want 0 during settings divergence", probeCount)
	}
}

func TestOverviewHealthCachesRecentProbeResult(t *testing.T) {
	modelLister := &countingOverviewHealthModelLister{
		models: []grok.Model{{ID: "grok-4.3"}},
	}
	handler := &Handler{
		Store:       newOverviewHealthTestStore(t),
		ModelLister: modelLister,
	}

	firstResponse, firstCompleted := handler.loadOverviewHealth(context.Background())
	secondResponse, secondCompleted := handler.loadOverviewHealth(context.Background())

	if !firstCompleted || !secondCompleted {
		t.Fatal("health request did not complete")
	}
	if firstResponse.Status != OverviewHealthHealthy || secondResponse.Status != OverviewHealthHealthy {
		t.Fatalf("health statuses = %q, %q; want healthy", firstResponse.Status, secondResponse.Status)
	}
	if !firstResponse.CheckedAt.Equal(secondResponse.CheckedAt) {
		t.Fatalf("cached checked_at changed from %s to %s", firstResponse.CheckedAt, secondResponse.CheckedAt)
	}
	if probeCount := modelLister.callCount.Load(); probeCount != 1 {
		t.Fatalf("upstream probe count = %d, want 1", probeCount)
	}
}

func TestOverviewHealthCoalescesConcurrentRequests(t *testing.T) {
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	modelLister := &countingOverviewHealthModelLister{
		models:       []grok.Model{{ID: "grok-4.3"}},
		probeStarted: probeStarted,
		releaseProbe: releaseProbe,
	}
	handler := &Handler{
		Store:       newOverviewHealthTestStore(t),
		ModelLister: modelLister,
	}

	const concurrentRequestCount = 12
	responses := make(chan OverviewHealthResponse, concurrentRequestCount)
	for requestIndex := 0; requestIndex < concurrentRequestCount; requestIndex++ {
		go func() {
			response, completed := handler.loadOverviewHealth(context.Background())
			if !completed {
				responses <- OverviewHealthResponse{Status: OverviewHealthUnknown}
				return
			}
			responses <- response
		}()
	}

	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream health probe did not start")
	}
	close(releaseProbe)

	for responseIndex := 0; responseIndex < concurrentRequestCount; responseIndex++ {
		select {
		case response := <-responses:
			if response.Status != OverviewHealthHealthy {
				t.Fatalf("health status = %q, want %q", response.Status, OverviewHealthHealthy)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent health request did not complete")
		}
	}
	if probeCount := modelLister.callCount.Load(); probeCount != 1 {
		t.Fatalf("concurrent upstream probe count = %d, want 1", probeCount)
	}
}

func TestOverviewHealthCacheCanBeInvalidated(t *testing.T) {
	modelLister := &countingOverviewHealthModelLister{
		models: []grok.Model{{ID: "grok-4.3"}},
	}
	handler := &Handler{
		Store:       newOverviewHealthTestStore(t),
		ModelLister: modelLister,
	}

	if _, completed := handler.loadOverviewHealth(context.Background()); !completed {
		t.Fatal("initial health request did not complete")
	}
	handler.invalidateOverviewHealthCache()
	if _, completed := handler.loadOverviewHealth(context.Background()); !completed {
		t.Fatal("health request after invalidation did not complete")
	}

	if probeCount := modelLister.callCount.Load(); probeCount != 2 {
		t.Fatalf("upstream probe count after invalidation = %d, want 2", probeCount)
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

func newOverviewHealthTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()

	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "overview-health-cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqliteStore.Close(); err != nil {
			t.Errorf("close overview health store: %v", err)
		}
	})
	if err := sqliteStore.ConfigureAPIKeyEncryption("overview-health-secret-at-least-32-bytes"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteStore.UpsertServerSettings(
		context.Background(),
		store.ServerSettings{Runtime: validOverviewHealthSettings()},
	); err != nil {
		t.Fatal(err)
	}
	return sqliteStore
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
