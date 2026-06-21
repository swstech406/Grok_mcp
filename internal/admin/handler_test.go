package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/store"
)

func testHandler(t *testing.T) (*Handler, store.Store) {
	t.Helper()
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &Handler{Store: s}, s
}

func withAdmin(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestAdminKeyCRUD(t *testing.T) {
	h, st := testHandler(t)
	mux := NewMux(h)
	admin := auth.AdminTokenMiddleware("admin-secret")

	ctx := context.Background()
	server := admin(mux)

	// Create
	body := `{"name":"ci","rate_limit":10}`
	req := withAdmin(httptest.NewRequest(http.MethodPost, "/admin/v1/keys", bytes.NewBufferString(body)), "admin-secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created CreateKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.APIKey == "" || created.Key.ID == "" {
		t.Fatalf("unexpected create response: %+v", created)
	}

	// List
	req2 := withAdmin(httptest.NewRequest(http.MethodGet, "/admin/v1/keys", nil), "admin-secret")
	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list: %d", rec2.Code)
	}

	// Get
	req3 := withAdmin(httptest.NewRequest(http.MethodGet, "/admin/v1/keys/"+created.Key.ID, nil), "admin-secret")
	rec3 := httptest.NewRecorder()
	server.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("get: %d", rec3.Code)
	}

	// Patch
	patch := `{"enabled":false}`
	req4 := withAdmin(httptest.NewRequest(http.MethodPatch, "/admin/v1/keys/"+created.Key.ID, bytes.NewBufferString(patch)), "admin-secret")
	rec4 := httptest.NewRecorder()
	server.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("patch: %d", rec4.Code)
	}

	_ = st.RecordUsage(ctx, store.UsageRecord{
		KeyID: created.Key.ID, ToolName: "grok_web_search", Timestamp: time.Now().UTC(),
	})

	req5 := withAdmin(httptest.NewRequest(http.MethodGet, "/admin/v1/keys/"+created.Key.ID+"/usage", nil), "admin-secret")
	rec5 := httptest.NewRecorder()
	server.ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusOK {
		t.Fatalf("usage: %d", rec5.Code)
	}

	req6 := withAdmin(httptest.NewRequest(http.MethodGet, "/admin/v1/stats", nil), "admin-secret")
	rec6 := httptest.NewRecorder()
	server.ServeHTTP(rec6, req6)
	if rec6.Code != http.StatusOK {
		t.Fatalf("stats: %d", rec6.Code)
	}

	// Delete
	req7 := withAdmin(httptest.NewRequest(http.MethodDelete, "/admin/v1/keys/"+created.Key.ID, nil), "admin-secret")
	rec7 := httptest.NewRecorder()
	server.ServeHTTP(rec7, req7)
	if rec7.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec7.Code)
	}
}