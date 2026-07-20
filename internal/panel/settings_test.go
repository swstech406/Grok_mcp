package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/version"
)

type recordingSettingsApplier struct {
	appliedSettings config.ServerSettings
	appliedRevision int64
	liveRevision    int64
	applyError      error
	duringApply     func()
}

type failingServerSettingsStore struct {
	store.TestStore
	persistenceError error
}

func (testStore failingServerSettingsStore) UpsertServerSettings(context.Context, store.ServerSettings) (*store.ServerSettings, error) {
	return nil, testStore.persistenceError
}

type blockingSettingsApplier struct {
	mutex                  sync.Mutex
	liveRevision           int64
	appliedModels          []string
	firstApplyStarted      chan struct{}
	releaseFirstApply      chan struct{}
	secondApplyStarted     chan struct{}
	firstApplyStartedOnce  sync.Once
	secondApplyStartedOnce sync.Once
}

func (applier *blockingSettingsApplier) ApplyServerSettings(settings config.ServerSettings, persistedRevision int64) error {
	applier.mutex.Lock()
	applyIndex := len(applier.appliedModels)
	applier.appliedModels = append(applier.appliedModels, settings.Model)
	applier.mutex.Unlock()

	if applyIndex == 0 {
		applier.firstApplyStartedOnce.Do(func() { close(applier.firstApplyStarted) })
		<-applier.releaseFirstApply
	} else if applyIndex == 1 {
		applier.secondApplyStartedOnce.Do(func() { close(applier.secondApplyStarted) })
	}
	applier.mutex.Lock()
	applier.liveRevision = persistedRevision
	applier.mutex.Unlock()
	return nil
}

func (applier *blockingSettingsApplier) LiveServerSettingsVersion() int64 {
	applier.mutex.Lock()
	defer applier.mutex.Unlock()
	return applier.liveRevision
}

func (applier *recordingSettingsApplier) ApplyServerSettings(settings config.ServerSettings, persistedRevision int64) error {
	applier.appliedSettings = settings
	applier.appliedRevision = persistedRevision
	if applier.duringApply != nil {
		applier.duringApply()
	}
	if applier.applyError != nil {
		return applier.applyError
	}
	applier.liveRevision = persistedRevision
	return nil
}

func (applier *recordingSettingsApplier) LiveServerSettingsVersion() int64 {
	return applier.liveRevision
}

func TestServerSettingsResponseNeverIncludesCPAAPIKey(t *testing.T) {
	const sensitiveAPIKey = "cpa-panel-never-return-this-full-secret-7f0d5b"
	response := toServerSettingsResponse(config.ServerSettings{
		CPABaseURL:               "https://cpa.example.test",
		CPAAPIKey:                sensitiveAPIKey,
		UpstreamProtocol:         config.UpstreamProtocolResponses,
		Model:                    "grok-4.3",
		OperationsMetricsEnabled: true,
	}, nil, 7, 7)
	if !response.OperationsMetricsEnabled {
		t.Fatal("panel settings response did not expose the enabled operational metrics setting")
	}

	encodedResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal server settings response: %v", err)
	}
	encodedText := string(encodedResponse)
	if strings.Contains(encodedText, sensitiveAPIKey) {
		t.Fatalf("panel settings response exposed CPA API key: %s", encodedText)
	}
	if strings.Contains(encodedText, `"cpa_api_key"`) {
		t.Fatalf("panel settings response included raw CPA API key field: %s", encodedText)
	}
}

func TestAdminUpdateServerSettingsKeepsInitialSettingsImmutable(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.ConfigureAPIKeyEncryption("panel-settings-test-secret"); err != nil {
		t.Fatal(err)
	}

	initialSettings := config.ServerSettings{
		CPABaseURL:                 "http://127.0.0.1:8317",
		CPAAPIKey:                  "initial-key",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		TimeoutSeconds:             120,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}
	if _, err := sqliteStore.UpsertServerSettings(context.Background(), store.ServerSettings{Runtime: initialSettings}); err != nil {
		t.Fatal(err)
	}

	settingsApplier := &recordingSettingsApplier{liveRevision: 1}
	handler := &Handler{
		Store:                 sqliteStore,
		InitialServerSettings: initialSettings,
		SettingsApplier:       settingsApplier,
	}
	settingsApplier.duringApply = func() {
		handler.overviewHealthState.mutex.Lock()
		defer handler.overviewHealthState.mutex.Unlock()
		handler.overviewHealthState.cachedResponse = OverviewHealthResponse{
			Status:    OverviewHealthUnknown,
			CheckedAt: time.Now().UTC(),
		}
		handler.overviewHealthState.cacheExpiresAt = time.Now().Add(time.Minute)
	}
	handler.overviewHealthState.cachedResponse = OverviewHealthResponse{
		Status:    OverviewHealthHealthy,
		CheckedAt: time.Now().UTC(),
	}
	handler.overviewHealthState.cacheExpiresAt = time.Now().Add(time.Minute)
	request := httptest.NewRequest(
		http.MethodPatch,
		"/panel/v1/admin/settings",
		bytes.NewBufferString(`{"model":"grok-4.4","mcp_global_search_concurrency":10,"mcp_user_search_concurrency":2,"operations_metrics_enabled":true}`),
	)
	responseRecorder := httptest.NewRecorder()

	handler.adminUpdateServerSettings(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("update settings status = %d, body = %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	var response ServerSettingsResponse
	if err := json.NewDecoder(responseRecorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode updated settings response: %v", err)
	}
	if response.Version != version.Version {
		t.Fatalf("response version = %q, want %q", response.Version, version.Version)
	}
	if response.PersistedVersion != 2 || response.LiveVersion != 2 || response.ApplyState != ServerSettingsApplied {
		t.Fatalf("response settings apply state = %+v", response)
	}
	if response.MCPGlobalSearchConcurrency != 10 || response.MCPUserSearchConcurrency != 2 {
		t.Fatalf("response search concurrency = %+v", response)
	}
	if !response.OperationsMetricsEnabled {
		t.Fatalf("response operations metrics setting = false, want true")
	}
	if handler.InitialServerSettings != initialSettings {
		t.Fatalf("initial settings mutated: before=%+v after=%+v", initialSettings, handler.InitialServerSettings)
	}
	if settingsApplier.appliedSettings.Model != "grok-4.4" {
		t.Fatalf("applied model = %q, want grok-4.4", settingsApplier.appliedSettings.Model)
	}
	if settingsApplier.appliedRevision != 2 {
		t.Fatalf("applied revision = %d, want 2", settingsApplier.appliedRevision)
	}
	if settingsApplier.appliedSettings.MCPGlobalSearchConcurrency != 10 || settingsApplier.appliedSettings.MCPUserSearchConcurrency != 2 {
		t.Fatalf("applied search concurrency = %+v", settingsApplier.appliedSettings)
	}
	if !settingsApplier.appliedSettings.OperationsMetricsEnabled {
		t.Fatalf("applied operations metrics setting = false, want true")
	}
	if !handler.overviewHealthState.cachedResponse.CheckedAt.IsZero() {
		t.Fatal("server settings update did not invalidate overview health cache")
	}

	storedSettings, err := sqliteStore.GetServerSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if storedSettings == nil || storedSettings.Model != "grok-4.4" {
		t.Fatalf("stored settings = %+v, want updated model", storedSettings)
	}
	if storedSettings.MCPGlobalSearchConcurrency != 10 || storedSettings.MCPUserSearchConcurrency != 2 {
		t.Fatalf("stored search concurrency = %+v", storedSettings)
	}
	if !storedSettings.OperationsMetricsEnabled {
		t.Fatalf("stored operations metrics setting = false, want true")
	}
}

func TestAdminUpdateServerSettingsReturnsSavedNotAppliedCondition(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "settings-apply-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.ConfigureAPIKeyEncryption("panel-settings-apply-failure-secret"); err != nil {
		t.Fatal(err)
	}

	initialSettings := config.ServerSettings{
		CPABaseURL:                 "http://127.0.0.1:8317",
		CPAAPIKey:                  "initial-key",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		TimeoutSeconds:             120,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}
	initialStoredSettings, err := sqliteStore.UpsertServerSettings(
		context.Background(),
		store.ServerSettings{Runtime: initialSettings},
	)
	if err != nil {
		t.Fatal(err)
	}

	settingsApplier := &recordingSettingsApplier{
		liveRevision: initialStoredSettings.Revision,
		applyError:   errors.New("runtime apply failed"),
	}
	handler := &Handler{
		Store:           sqliteStore,
		SettingsApplier: settingsApplier,
	}
	request := httptest.NewRequest(
		http.MethodPatch,
		"/panel/v1/admin/settings",
		bytes.NewBufferString(`{"model":"grok-4.4"}`),
	)
	responseRecorder := httptest.NewRecorder()

	handler.adminUpdateServerSettings(responseRecorder, request)

	if responseRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("update settings status = %d, body = %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	var errorResponse savedNotAppliedErrorResponse
	if err := json.NewDecoder(responseRecorder.Body).Decode(&errorResponse); err != nil {
		t.Fatalf("decode saved-not-applied response: %v", err)
	}
	if errorResponse.Code != settingsSavedNotAppliedCode {
		t.Fatalf("error code = %q, want %q", errorResponse.Code, settingsSavedNotAppliedCode)
	}
	if errorResponse.PersistedVersion != 2 || errorResponse.LiveVersion != 1 {
		t.Fatalf("saved-not-applied versions = %+v", errorResponse)
	}
	if strings.Contains(responseRecorder.Body.String(), initialSettings.CPAAPIKey) {
		t.Fatalf("saved-not-applied response exposed CPA API key: %s", responseRecorder.Body.String())
	}

	persistedSettings, err := sqliteStore.GetServerSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persistedSettings.Model != "grok-4.4" || persistedSettings.Revision != 2 {
		t.Fatalf("persisted settings after apply failure = %+v", persistedSettings)
	}
	if settingsApplier.LiveServerSettingsVersion() != 1 {
		t.Fatalf("live revision advanced after apply failure: %d", settingsApplier.LiveServerSettingsVersion())
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/panel/v1/admin/settings", nil)
	getResponseRecorder := httptest.NewRecorder()
	handler.adminGetServerSettings(getResponseRecorder, getRequest)

	var getResponse ServerSettingsResponse
	if err := json.NewDecoder(getResponseRecorder.Body).Decode(&getResponse); err != nil {
		t.Fatalf("decode divergent settings response: %v", err)
	}
	if getResponse.Model != "grok-4.4" {
		t.Fatalf("GET model = %q, want persisted model grok-4.4", getResponse.Model)
	}
	if getResponse.PersistedVersion != 2 || getResponse.LiveVersion != 1 || getResponse.ApplyState != ServerSettingsSavedNotApplied {
		t.Fatalf("GET divergent settings state = %+v", getResponse)
	}
}

func TestAdminUpdateServerSettingsKeepsPersistenceFailureDistinct(t *testing.T) {
	initialSettings := config.ServerSettings{
		CPABaseURL:                 "http://127.0.0.1:8317",
		CPAAPIKey:                  "initial-key",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		TimeoutSeconds:             120,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}
	settingsApplier := &recordingSettingsApplier{liveRevision: 1}
	handler := &Handler{
		Store: failingServerSettingsStore{
			persistenceError: errors.New("database unavailable"),
		},
		InitialServerSettings: initialSettings,
		SettingsApplier:       settingsApplier,
	}
	request := httptest.NewRequest(
		http.MethodPatch,
		"/panel/v1/admin/settings",
		bytes.NewBufferString(`{"model":"grok-4.4"}`),
	)
	responseRecorder := httptest.NewRecorder()

	handler.adminUpdateServerSettings(responseRecorder, request)

	if responseRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("update settings status = %d, body = %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	var response errorResponse
	if err := json.NewDecoder(responseRecorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode persistence failure response: %v", err)
	}
	if response.Code != "internal_error" || response.Error != "failed to save server settings" {
		t.Fatalf("persistence failure response = %+v", response)
	}
	if settingsApplier.appliedRevision != 0 {
		t.Fatalf("runtime apply was invoked after persistence failure with revision %d", settingsApplier.appliedRevision)
	}
}

func TestAdminUpdateServerSettingsSerializesPersistAndApplyOrder(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "settings-concurrent-updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.ConfigureAPIKeyEncryption("panel-settings-concurrent-secret"); err != nil {
		t.Fatal(err)
	}

	initialSettings := config.ServerSettings{
		CPABaseURL:                 "http://127.0.0.1:8317",
		CPAAPIKey:                  "initial-key",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		TimeoutSeconds:             120,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}
	if _, err := sqliteStore.UpsertServerSettings(
		context.Background(),
		store.ServerSettings{Runtime: initialSettings},
	); err != nil {
		t.Fatal(err)
	}

	settingsApplier := &blockingSettingsApplier{
		liveRevision:       1,
		firstApplyStarted:  make(chan struct{}),
		releaseFirstApply:  make(chan struct{}),
		secondApplyStarted: make(chan struct{}),
	}
	handler := &Handler{Store: sqliteStore, SettingsApplier: settingsApplier}
	firstResponse := httptest.NewRecorder()
	secondResponse := httptest.NewRecorder()
	firstCompleted := make(chan struct{})
	secondRequestStarted := make(chan struct{})
	secondCompleted := make(chan struct{})

	go func() {
		defer close(firstCompleted)
		handler.adminUpdateServerSettings(
			firstResponse,
			httptest.NewRequest(http.MethodPatch, "/panel/v1/admin/settings", bytes.NewBufferString(`{"model":"grok-4.4"}`)),
		)
	}()
	select {
	case <-settingsApplier.firstApplyStarted:
	case <-time.After(time.Second):
		t.Fatal("first settings apply did not start")
	}

	go func() {
		close(secondRequestStarted)
		defer close(secondCompleted)
		handler.adminUpdateServerSettings(
			secondResponse,
			httptest.NewRequest(http.MethodPatch, "/panel/v1/admin/settings", bytes.NewBufferString(`{"model":"grok-4.5"}`)),
		)
	}()
	<-secondRequestStarted
	select {
	case <-settingsApplier.secondApplyStarted:
		t.Fatal("second settings apply started before the first update completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(settingsApplier.releaseFirstApply)
	for _, completed := range []<-chan struct{}{firstCompleted, secondCompleted} {
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatal("serialized settings update did not complete")
		}
	}
	if firstResponse.Code != http.StatusOK || secondResponse.Code != http.StatusOK {
		t.Fatalf("serialized update statuses = %d and %d", firstResponse.Code, secondResponse.Code)
	}

	persistedSettings, err := sqliteStore.GetServerSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persistedSettings.Model != "grok-4.5" || persistedSettings.Revision != 3 {
		t.Fatalf("final persisted settings = %+v", persistedSettings)
	}
	if liveVersion := settingsApplier.LiveServerSettingsVersion(); liveVersion != 3 {
		t.Fatalf("final live version = %d, want 3", liveVersion)
	}
	settingsApplier.mutex.Lock()
	appliedModels := append([]string(nil), settingsApplier.appliedModels...)
	settingsApplier.mutex.Unlock()
	if len(appliedModels) != 2 || appliedModels[0] != "grok-4.4" || appliedModels[1] != "grok-4.5" {
		t.Fatalf("applied model order = %v", appliedModels)
	}
}

func TestAdminUpdateServerSettingsRejectsInvalidSearchConcurrency(t *testing.T) {
	initialSettings := config.ServerSettings{
		CPABaseURL:                 "http://127.0.0.1:8317",
		CPAAPIKey:                  "initial-key",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		TimeoutSeconds:             120,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}
	handler := &Handler{
		Store:                 store.TestStore{},
		InitialServerSettings: initialSettings,
	}

	testCases := []struct {
		name    string
		payload string
	}{
		{name: "zero global", payload: `{"mcp_global_search_concurrency":0}`},
		{name: "user exceeds global", payload: `{"mcp_global_search_concurrency":2,"mcp_user_search_concurrency":3}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPatch,
				"/panel/v1/admin/settings",
				bytes.NewBufferString(testCase.payload),
			)
			responseRecorder := httptest.NewRecorder()

			handler.adminUpdateServerSettings(responseRecorder, request)

			if responseRecorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", responseRecorder.Code, responseRecorder.Body.String())
			}
		})
	}
}
