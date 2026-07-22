package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type failingBootstrapCreationStore struct {
	store.TestStore
}

func (failingBootstrapCreationStore) CreateUser(context.Context, string, string, store.UserRole) (*store.User, error) {
	return nil, errors.New("simulated database failure")
}

type recordingRuntimeSettingsApplier struct {
	appliedSettings config.ServerSettings
	applyError      error
}

func (applier *recordingRuntimeSettingsApplier) ApplyServerSettings(settings config.ServerSettings) error {
	applier.appliedSettings = settings
	return applier.applyError
}

func TestRuntimeServerSettingsApplierKeepsConfirmedVersionAfterFailure(t *testing.T) {
	upstreamApplier := &recordingRuntimeSettingsApplier{applyError: errors.New("upstream apply failed")}
	applier := &runtimeServerSettingsApplier{upstreamApplier: upstreamApplier}
	applier.liveVersion.Store(5)

	err := applier.ApplyServerSettings(config.ServerSettings{}, 6)

	if err == nil {
		t.Fatal("ApplyServerSettings succeeded, want upstream failure")
	}
	if liveVersion := applier.LiveServerSettingsVersion(); liveVersion != 5 {
		t.Fatalf("live settings version = %d, want prior confirmed version 5", liveVersion)
	}
}

func TestRuntimeServerSettingsApplierUpdatesSearchConcurrency(t *testing.T) {
	searchConcurrencyLimiter := ratelimit.NewSearchConcurrencyLimiter(1, 1)
	defer searchConcurrencyLimiter.Close()

	upstreamApplier := &recordingRuntimeSettingsApplier{}
	applier := &runtimeServerSettingsApplier{
		upstreamApplier:          upstreamApplier,
		searchConcurrencyLimiter: searchConcurrencyLimiter,
	}
	settings := config.ServerSettings{
		MCPGlobalSearchConcurrency: 2,
		MCPUserSearchConcurrency:   2,
	}
	if err := applier.ApplyServerSettings(settings, 7); err != nil {
		t.Fatalf("ApplyServerSettings failed: %v", err)
	}
	if liveVersion := applier.LiveServerSettingsVersion(); liveVersion != 7 {
		t.Fatalf("live settings version = %d, want 7", liveVersion)
	}
	if upstreamApplier.appliedSettings != settings {
		t.Fatalf("upstream applied settings = %+v, want %+v", upstreamApplier.appliedSettings, settings)
	}

	globalLimit, perUserLimit := searchConcurrencyLimiter.Limits()
	if globalLimit != 2 || perUserLimit != 2 {
		t.Fatalf("limiter limits = (%d, %d), want (2, 2)", globalLimit, perUserLimit)
	}
}

func TestEnsureBootstrapAdminCreatesAdminForEmptyStore(t *testing.T) {
	temporaryDirectory := t.TempDir()
	storePath := filepath.Join(temporaryDirectory, "bootstrap.db")
	credentialPath := filepath.Join(temporaryDirectory, "bootstrap-admin.json")
	sqliteStore, err := store.OpenSQLite(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	credentials, err := EnsureBootstrapAdmin(context.Background(), sqliteStore, credentialPath)
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
	credentialInfo, err := os.Stat(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if credentialInfo.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap credential permissions = %#o, want 0600", credentialInfo.Mode().Perm())
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

	secondCredentials, err := EnsureBootstrapAdmin(context.Background(), sqliteStore, credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if secondCredentials != nil {
		t.Fatalf("expected no credentials when an enabled admin already exists, got %+v", secondCredentials)
	}
}

func TestEnsureBootstrapAdminCreatesAdminWhenOnlyRegularUsersExist(t *testing.T) {
	temporaryDirectory := t.TempDir()
	storePath := filepath.Join(temporaryDirectory, "bootstrap-non-empty.db")
	credentialPath := filepath.Join(temporaryDirectory, "bootstrap-admin.json")
	sqliteStore, err := store.OpenSQLite(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	if _, err := sqliteStore.CreateUser(context.Background(), "existing", "hash", store.RoleUser); err != nil {
		t.Fatal(err)
	}

	credentials, err := EnsureBootstrapAdmin(context.Background(), sqliteStore, credentialPath)
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

func TestEnsureBootstrapAdminDoesNotCreateCredentialFileForExistingAdmin(t *testing.T) {
	temporaryDirectory := t.TempDir()
	credentialPath := filepath.Join(temporaryDirectory, "bootstrap-admin.json")
	sqliteStore, err := store.OpenSQLite(filepath.Join(temporaryDirectory, "existing-admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if _, err := sqliteStore.CreateUser(context.Background(), "existing-admin", "hash", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	credentials, err := EnsureBootstrapAdmin(context.Background(), sqliteStore, credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if credentials != nil {
		t.Fatalf("unexpected bootstrap credentials: %+v", credentials)
	}
	if _, err := os.Stat(credentialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential path should remain absent, got %v", err)
	}
}

func TestEnsureBootstrapAdminReusesCredentialFileAfterDatabaseFailure(t *testing.T) {
	temporaryDirectory := t.TempDir()
	credentialPath := filepath.Join(temporaryDirectory, "bootstrap-admin.json")

	if _, err := EnsureBootstrapAdmin(context.Background(), failingBootstrapCreationStore{}, credentialPath); err == nil {
		t.Fatal("expected simulated database failure")
	}
	credentialJSON, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	var persistedCredentials BootstrapAdminCredentials
	if err := json.Unmarshal(credentialJSON, &persistedCredentials); err != nil {
		t.Fatal(err)
	}

	sqliteStore, err := store.OpenSQLite(filepath.Join(temporaryDirectory, "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	recoveredCredentials, err := EnsureBootstrapAdmin(context.Background(), sqliteStore, credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredCredentials.Password != persistedCredentials.Password {
		t.Fatal("bootstrap recovery generated a different password")
	}
}

func TestBootstrapCredentialFileRejectsUnsafeExistingPaths(t *testing.T) {
	credentialJSON := []byte(`{"username":"admin","password":"safe-password-marker"}`)

	t.Run("insecure permissions", func(t *testing.T) {
		credentialPath := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(credentialPath, credentialJSON, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadOrCreateBootstrapCredentials(credentialPath); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("expected permission rejection, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		temporaryDirectory := t.TempDir()
		targetPath := filepath.Join(temporaryDirectory, "target.json")
		credentialPath := filepath.Join(temporaryDirectory, "credentials.json")
		if err := os.WriteFile(targetPath, credentialJSON, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(targetPath, credentialPath); err != nil {
			t.Fatal(err)
		}
		if _, err := loadOrCreateBootstrapCredentials(credentialPath); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		if _, err := loadOrCreateBootstrapCredentials(t.TempDir()); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("expected directory rejection, got %v", err)
		}
	})
}

func TestBootstrapCredentialFileRejectsMalformedAndOversizedJSON(t *testing.T) {
	testCases := []struct {
		name    string
		content []byte
	}{
		{name: "malformed", content: []byte(`{"username":"admin"`)},
		{name: "unknown field", content: []byte(`{"username":"admin","password":"safe-password","extra":true}`)},
		{name: "oversized", content: bytes.Repeat([]byte("x"), maximumBootstrapFileBytes+1)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			credentialPath := filepath.Join(t.TempDir(), "credentials.json")
			if err := os.WriteFile(credentialPath, testCase.content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOrCreateBootstrapCredentials(credentialPath); err == nil {
				t.Fatal("expected unsafe credential content to be rejected")
			}
		})
	}
}

func TestBootstrapCredentialWriteFailureRemovesIncompleteFile(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credentialPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnlyFile, err := os.Open(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeNewBootstrapCredentials(credentialPath, readOnlyFile); err == nil {
		t.Fatal("expected write through read-only file descriptor to fail")
	}
	if _, err := os.Stat(credentialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete credential file was not removed: %v", err)
	}
}

func TestBootstrapCredentialLogContainsPathButNotPassword(t *testing.T) {
	const credentialPath = "/secure/bootstrap-admin.json"
	const passwordMarker = "bootstrap-password-must-never-appear"
	var logBuffer bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logBuffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	logBootstrapCredentialsAvailable(credentialPath)
	logOutput := logBuffer.String()
	if !strings.Contains(logOutput, credentialPath) {
		t.Fatalf("bootstrap log does not contain credential path: %q", logOutput)
	}
	if strings.Contains(logOutput, passwordMarker) || strings.Contains(logOutput, "password=") {
		t.Fatalf("bootstrap log contains password material: %q", logOutput)
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
		"worker-src 'self'",
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
	databasePath := filepath.Join(t.TempDir(), "environment-settings.db")
	sqliteStore, err := store.OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.ConfigureAPIKeyEncryption("app-test-settings-encryption-secret-at-least-32-bytes"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		CPABaseURL:                 " http://127.0.0.1:8317/ ",
		CPAAPIKey:                  " environment-key ",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      " grok-4.3 ",
		Timeout:                    45 * time.Second,
		MCPGlobalSearchConcurrency: 12,
		MCPUserSearchConcurrency:   3,
		RegistrationMode:           store.RegistrationModeFree,
	}
	originalConfig := *cfg

	storedSettings, err := InitializeServerSettings(context.Background(), sqliteStore, cfg)
	if err != nil {
		t.Fatal(err)
	}
	effectiveSettings := storedSettings.Runtime
	if storedSettings.Revision != 1 {
		t.Fatalf("initialized settings revision = %d, want 1", storedSettings.Revision)
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

	persistedSettings, err := sqliteStore.GetServerSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persistedSettings == nil || persistedSettings.CPAAPIKey != "environment-key" {
		t.Fatalf("expected normalized settings to be persisted, got %+v", persistedSettings)
	}

	rawDatabase, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDatabase.Close()
	var ciphertextAPIKey string
	if err := rawDatabase.QueryRow(`
		SELECT cpa_api_key_ciphertext
		FROM server_settings`,
	).Scan(&ciphertextAPIKey); err != nil {
		t.Fatal(err)
	}
	if ciphertextAPIKey == "" || ciphertextAPIKey == "environment-key" {
		t.Fatalf("unexpected CPA API key ciphertext: %q", ciphertextAPIKey)
	}
}

func TestInitializeServerSettingsUsesInitialRegistrationModeOnlyForNewSettings(t *testing.T) {
	for _, initialRegistrationMode := range []store.RegistrationMode{
		store.RegistrationModeDisabled,
		store.RegistrationModeInvite,
		store.RegistrationModeFree,
	} {
		t.Run(string(initialRegistrationMode), func(t *testing.T) {
			sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "initial-registration.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer sqliteStore.Close()
			if err := sqliteStore.ConfigureAPIKeyEncryption("app-test-settings-encryption-secret-at-least-32-bytes"); err != nil {
				t.Fatal(err)
			}

			cfg := &config.Config{
				CPABaseURL:                 "http://127.0.0.1:8317",
				CPAAPIKey:                  "environment-key",
				UpstreamProtocol:           config.UpstreamProtocolResponses,
				Model:                      "grok-4.3",
				Timeout:                    30 * time.Second,
				MCPGlobalSearchConcurrency: 16,
				MCPUserSearchConcurrency:   4,
				RegistrationMode:           initialRegistrationMode,
			}

			storedSettings, err := InitializeServerSettings(context.Background(), sqliteStore, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if storedSettings.RegistrationMode != initialRegistrationMode {
				t.Fatalf("persisted registration mode = %q, want %q", storedSettings.RegistrationMode, initialRegistrationMode)
			}
		})
	}
}

func TestInitializeServerSettingsPrefersDatabaseAndSuppliesMissingEnvironmentKey(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "database-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.ConfigureAPIKeyEncryption("app-test-settings-encryption-secret-at-least-32-bytes"); err != nil {
		t.Fatal(err)
	}

	databaseSettings := config.ServerSettings{
		CPABaseURL:                 "http://database-cpa.example",
		CPAAPIKey:                  "database-key",
		UpstreamProtocol:           config.UpstreamProtocolAnthropicMessages,
		Model:                      "grok-database-model",
		TimeoutSeconds:             90,
		MCPGlobalSearchConcurrency: 8,
		MCPUserSearchConcurrency:   2,
		RegistrationMode:           store.RegistrationModeInvite,
		Debug:                      true,
	}
	if _, err := sqliteStore.UpsertServerSettings(context.Background(), store.ServerSettings{Runtime: databaseSettings}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		CPABaseURL:                 "http://environment-cpa.example",
		CPAAPIKey:                  "",
		UpstreamProtocol:           config.UpstreamProtocolChatCompletions,
		Model:                      "grok-environment-model",
		Timeout:                    30 * time.Second,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeDisabled,
	}
	storedSettings, err := InitializeServerSettings(context.Background(), sqliteStore, cfg)
	if err != nil {
		t.Fatal(err)
	}
	effectiveSettings := storedSettings.Runtime
	if storedSettings.Revision != 2 {
		t.Fatalf("restarted settings revision = %d, want 2", storedSettings.Revision)
	}
	if effectiveSettings != databaseSettings {
		t.Fatalf("effective settings = %+v, want database settings %+v", effectiveSettings, databaseSettings)
	}
	if cfg.CPAAPIKey != "" {
		t.Fatalf("database settings must not be copied into startup config, got key %q", cfg.CPAAPIKey)
	}
}

func TestInitializeServerSettingsBackfillsLegacySearchConcurrencySentinels(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy-search-concurrency.db")
	sqliteStore, err := store.OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.ConfigureAPIKeyEncryption("app-test-settings-encryption-secret-at-least-32-bytes"); err != nil {
		t.Fatal(err)
	}

	legacySettings := config.ServerSettings{
		CPABaseURL:                 "http://database-cpa.example",
		CPAAPIKey:                  "database-key",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-database-model",
		TimeoutSeconds:             90,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}
	if _, err := sqliteStore.UpsertServerSettings(context.Background(), store.ServerSettings{Runtime: legacySettings}); err != nil {
		t.Fatal(err)
	}

	rawDatabase, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDatabase.Exec(`
		UPDATE server_settings
		SET mcp_global_search_concurrency = 0,
			mcp_user_search_concurrency = 0`); err != nil {
		_ = rawDatabase.Close()
		t.Fatal(err)
	}
	if err := rawDatabase.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		CPABaseURL:                 "http://environment-cpa.example",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-environment-model",
		Timeout:                    30 * time.Second,
		MCPGlobalSearchConcurrency: 9,
		MCPUserSearchConcurrency:   3,
		RegistrationMode:           store.RegistrationModeFree,
	}
	storedSettings, err := InitializeServerSettings(context.Background(), sqliteStore, cfg)
	if err != nil {
		t.Fatal(err)
	}
	effectiveSettings := storedSettings.Runtime
	if effectiveSettings.MCPGlobalSearchConcurrency != 9 || effectiveSettings.MCPUserSearchConcurrency != 3 {
		t.Fatalf("effective search concurrency = %+v", effectiveSettings)
	}

	persistedSettings, err := sqliteStore.GetServerSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persistedSettings.MCPGlobalSearchConcurrency != 9 || persistedSettings.MCPUserSearchConcurrency != 3 {
		t.Fatalf("persisted search concurrency = %+v", persistedSettings)
	}
}

func TestInitializeServerSettingsRejectsMissingAPIKeyWithoutDatabaseFallback(t *testing.T) {
	sqliteStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-key.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.ConfigureAPIKeyEncryption("app-test-settings-encryption-secret-at-least-32-bytes"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		CPABaseURL:                 "http://127.0.0.1:8317",
		UpstreamProtocol:           config.UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		Timeout:                    30 * time.Second,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}
	_, err = InitializeServerSettings(context.Background(), sqliteStore, cfg)
	if err == nil || !strings.Contains(err.Error(), "CPA_API_KEY is required") {
		t.Fatalf("expected missing CPA key error, got %v", err)
	}
}
