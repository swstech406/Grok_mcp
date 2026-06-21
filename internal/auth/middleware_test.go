package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grok-mcp/internal/store"
)

type memStore struct {
	byHash map[string]*store.APIKey
}

func (m *memStore) Close() error { return nil }
func (m *memStore) CreateKey(ctx context.Context, name string, rateLimit int) (*store.APIKey, string, error) {
	return nil, "", nil
}
func (m *memStore) GetKeyByHash(ctx context.Context, hash string) (*store.APIKey, error) {
	return m.byHash[hash], nil
}
func (m *memStore) ListKeys(ctx context.Context) ([]*store.APIKey, error) { return nil, nil }
func (m *memStore) GetKeyByID(ctx context.Context, id string) (*store.APIKey, error) {
	return nil, nil
}
func (m *memStore) UpdateKey(ctx context.Context, id string, updates store.KeyUpdates) (*store.APIKey, error) {
	return nil, nil
}
func (m *memStore) DeleteKey(ctx context.Context, id string) error { return nil }
func (m *memStore) RecordUsage(ctx context.Context, record store.UsageRecord) error {
	return nil
}
func (m *memStore) GetUsageStats(ctx context.Context, keyID string, since time.Time) (*store.UsageStats, error) {
	return nil, nil
}
func (m *memStore) GetGlobalStats(ctx context.Context, since time.Time) (*store.UsageStats, error) {
	return nil, nil
}
func (m *memStore) TouchKeyUsage(ctx context.Context, keyID string) error { return nil }

func TestAPIKeyMiddleware(t *testing.T) {
	raw := "grok_testtoken"
	hash := store.HashAPIKey(raw)
	st := &memStore{byHash: map[string]*store.APIKey{
		hash: {ID: "id-1", Enabled: true},
	}}

	var gotID string
	h := APIKeyMiddleware(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, ok := APIKeyFromContext(r.Context())
		if !ok {
			t.Fatal("missing key in context")
		}
		gotID = k.ID
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotID != "id-1" {
		t.Fatalf("code=%d id=%s", rec.Code, gotID)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec2.Code)
	}
}

func TestAdminTokenMiddleware(t *testing.T) {
	h := AdminTokenMiddleware("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec2.Code)
	}
}
