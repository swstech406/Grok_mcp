package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestEnsureBootstrapAdminCreatesAdminForEmptyStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "bootstrap.db")
	sqliteStore, err := store.OpenSQLite(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	credentials, err := EnsureBootstrapAdmin(context.Background(), sqliteStore)
	if err != nil {
		t.Fatal(err)
	}
	if credentials == nil {
		t.Fatalf("expected bootstrap credentials for an empty database")
	}
	if credentials.Username != bootstrapAdminUsername {
		t.Fatalf("bootstrap username = %q, want %q", credentials.Username, bootstrapAdminUsername)
	}
	if len(credentials.Password) != 12 {
		t.Fatalf("bootstrap password length = %d, want 12", len(credentials.Password))
	}

	adminUser, err := sqliteStore.GetUserByUsername(context.Background(), bootstrapAdminUsername)
	if err != nil {
		t.Fatal(err)
	}
	if adminUser == nil || adminUser.Role != store.RoleAdmin {
		t.Fatalf("expected created admin user, got %+v", adminUser)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(adminUser.PasswordHash), []byte(credentials.Password)); err != nil {
		t.Fatalf("bootstrap password does not match stored hash: %v", err)
	}

	secondCredentials, err := EnsureBootstrapAdmin(context.Background(), sqliteStore)
	if err != nil {
		t.Fatal(err)
	}
	if secondCredentials != nil {
		t.Fatalf("expected no credentials when an enabled admin already exists, got %+v", secondCredentials)
	}
}

func TestEnsureBootstrapAdminCreatesAdminWhenOnlyRegularUsersExist(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "bootstrap-non-empty.db")
	sqliteStore, err := store.OpenSQLite(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	if _, err := sqliteStore.CreateUser(context.Background(), "existing", "hash", store.RoleUser); err != nil {
		t.Fatal(err)
	}

	credentials, err := EnsureBootstrapAdmin(context.Background(), sqliteStore)
	if err != nil {
		t.Fatal(err)
	}
	if credentials == nil {
		t.Fatalf("expected bootstrap credentials when no enabled admin exists")
	}

	adminUser, err := sqliteStore.GetUserByUsername(context.Background(), bootstrapAdminUsername)
	if err != nil {
		t.Fatal(err)
	}
	if adminUser == nil || adminUser.Role != store.RoleAdmin || !adminUser.Enabled {
		t.Fatalf("expected enabled bootstrap admin user, got %+v", adminUser)
	}
}

func TestSecurityHeadersAllowPanelExternalAssets(t *testing.T) {
	handler := SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panel/", nil)

	handler.ServeHTTP(recorder, request)

	contentSecurityPolicyHeader := recorder.Header().Get("Content-Security-Policy")
	expectedDirectives := []string{
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src 'self' https://fonts.gstatic.com data:",
		"img-src 'self' data: blob: https:",
	}
	for _, expectedDirective := range expectedDirectives {
		if !strings.Contains(contentSecurityPolicyHeader, expectedDirective) {
			t.Fatalf("CSP header %q does not contain %q", contentSecurityPolicyHeader, expectedDirective)
		}
	}
}

func TestInitializeServerSettingsUsesEnvironmentDefaultsWithoutMutatingConfig(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "environment-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	cfg := &config.Config{
		CPABaseURL:       " http://127.0.0.1:8317/ ",
		CPAAPIKey:        " environment-key ",
		Model:            " grok-4.3 ",
		Timeout:          45 * time.Second,
		RegistrationMode: store.RegistrationModeFree,
	}
	originalConfig := *cfg

	effectiveSettings, err := InitializeServerSettings(context.Background(), sqliteStore, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if effectiveSettings.CPABaseURL != "http://127.0.0.1:8317" {
		t.Fatalf("effective base URL = %q", effectiveSettings.CPABaseURL)
	}
	if effectiveSettings.CPAAPIKey != "environment-key" {
		t.Fatalf("effective API key = %q", effectiveSettings.CPAAPIKey)
	}
	if effectiveSettings.Model != "grok-4.3" {
		t.Fatalf("effective model = %q", effectiveSettings.Model)
	}
	if !reflect.DeepEqual(*cfg, originalConfig) {
		t.Fatalf("InitializeServerSettings mutated startup config: before=%+v after=%+v", originalConfig, *cfg)
	}

	storedSettings, err := sqliteStore.GetServerSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if storedSettings == nil || storedSettings.CPAAPIKey != "environment-key" {
		t.Fatalf("expected normalized settings to be persisted, got %+v", storedSettings)
	}
}

func TestInitializeServerSettingsPrefersDatabaseAndSuppliesMissingEnvironmentKey(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "database-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	databaseSettings := config.ServerSettings{
		CPABaseURL:       "http://database-cpa.example",
		CPAAPIKey:        "database-key",
		Model:            "grok-database-model",
		TimeoutSeconds:   90,
		RegistrationMode: store.RegistrationModeInvite,
		Debug:            true,
	}
	if _, err := sqliteStore.UpsertServerSettings(context.Background(), config.StoreServerSettings(databaseSettings)); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		CPABaseURL: "http://environment-cpa.example",
		CPAAPIKey:  "",
		Model:      "grok-environment-model",
		Timeout:    30 * time.Second,
	}
	effectiveSettings, err := InitializeServerSettings(context.Background(), sqliteStore, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if effectiveSettings != databaseSettings {
		t.Fatalf("effective settings = %+v, want database settings %+v", effectiveSettings, databaseSettings)
	}
	if cfg.CPAAPIKey != "" {
		t.Fatalf("database settings must not be copied into startup config, got key %q", cfg.CPAAPIKey)
	}
}

func TestInitializeServerSettingsRejectsMissingAPIKeyWithoutDatabaseFallback(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-key.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	cfg := &config.Config{
		CPABaseURL: "http://127.0.0.1:8317",
		Model:      "grok-4.3",
		Timeout:    30 * time.Second,
	}
	_, err = InitializeServerSettings(context.Background(), sqliteStore, cfg)
	if err == nil || !strings.Contains(err.Error(), "CPA_API_KEY is required") {
		t.Fatalf("expected missing CPA key error, got %v", err)
	}
}
