package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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

	credentials, err := ensureBootstrapAdmin(context.Background(), sqliteStore)
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

	secondCredentials, err := ensureBootstrapAdmin(context.Background(), sqliteStore)
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

	credentials, err := ensureBootstrapAdmin(context.Background(), sqliteStore)
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
	handler := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
