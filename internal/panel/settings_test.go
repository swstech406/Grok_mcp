package panel

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/store"
)

type recordingSettingsApplier struct {
	appliedSettings config.ServerSettings
}

func (applier *recordingSettingsApplier) ApplyServerSettings(settings config.ServerSettings) error {
	applier.appliedSettings = settings
	return nil
}

func TestAdminUpdateServerSettingsKeepsInitialSettingsImmutable(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	initialSettings := config.ServerSettings{
		CPABaseURL:       "http://127.0.0.1:8317",
		CPAAPIKey:        "initial-key",
		Model:            "grok-4.3",
		TimeoutSeconds:   120,
		RegistrationMode: store.RegistrationModeFree,
	}
	if _, err := sqliteStore.UpsertServerSettings(context.Background(), config.StoreServerSettings(initialSettings)); err != nil {
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
		bytes.NewBufferString(`{"model":"grok-4.4"}`),
	)
	responseRecorder := httptest.NewRecorder()

	handler.adminUpdateServerSettings(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("update settings status = %d, body = %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	if handler.InitialServerSettings != initialSettings {
		t.Fatalf("initial settings mutated: before=%+v after=%+v", initialSettings, handler.InitialServerSettings)
	}
	if settingsApplier.appliedSettings.Model != "grok-4.4" {
		t.Fatalf("applied model = %q, want grok-4.4", settingsApplier.appliedSettings.Model)
	}

	storedSettings, err := sqliteStore.GetServerSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if storedSettings == nil || storedSettings.Model != "grok-4.4" {
		t.Fatalf("stored settings = %+v, want updated model", storedSettings)
	}
}
