package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/version"
)

type recordingSettingsApplier struct {
	appliedSettings config.ServerSettings
}

func (applier *recordingSettingsApplier) ApplyServerSettings(settings config.ServerSettings) error {
	applier.appliedSettings = settings
	return nil
}

func TestServerSettingsResponseNeverIncludesCPAAPIKey(t *testing.T) {
	const sensitiveAPIKey = "cpa-panel-never-return-this-full-secret-7f0d5b"
	response := toServerSettingsResponse(config.ServerSettings{
		CPABaseURL:       "https://cpa.example.test",
		CPAAPIKey:        sensitiveAPIKey,
		UpstreamProtocol: config.UpstreamProtocolResponses,
		Model:            "grok-4.3",
	}, nil)

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

	settingsApplier := &recordingSettingsApplier{}
	handler := &Handler{
		Store:                 sqliteStore,
		InitialServerSettings: initialSettings,
		SettingsApplier:       settingsApplier,
	}
	request := httptest.NewRequest(
		http.MethodPatch,
		"/panel/v1/admin/settings",
		bytes.NewBufferString(`{"model":"grok-4.4","mcp_global_search_concurrency":10,"mcp_user_search_concurrency":2}`),
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
	if response.MCPGlobalSearchConcurrency != 10 || response.MCPUserSearchConcurrency != 2 {
		t.Fatalf("response search concurrency = %+v", response)
	}
	if handler.InitialServerSettings != initialSettings {
		t.Fatalf("initial settings mutated: before=%+v after=%+v", initialSettings, handler.InitialServerSettings)
	}
	if settingsApplier.appliedSettings.Model != "grok-4.4" {
		t.Fatalf("applied model = %q, want grok-4.4", settingsApplier.appliedSettings.Model)
	}
	if settingsApplier.appliedSettings.MCPGlobalSearchConcurrency != 10 || settingsApplier.appliedSettings.MCPUserSearchConcurrency != 2 {
		t.Fatalf("applied search concurrency = %+v", settingsApplier.appliedSettings)
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
