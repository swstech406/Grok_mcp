package grok_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
)

func TestFilterGrokModelsKeepsOnlyGrokModels(t *testing.T) {
	models := []grok.Model{
		{ID: "grok-4.3"},
		{ID: " gpt-4 "},
		{ID: "Grok-Beta"},
		{ID: "grok-imagine-image"},
		{ID: "GROK-IMAGINE-IMAGE"},
		{ID: "grok-imagine-video-1.5-preview"},
		{ID: "grok-video-preview"},
		{ID: "   "},
		{ID: "grok-4.3"},
		{ID: "custom-grok-search"},
	}

	filteredModels := grok.FilterGrokModels(models)
	actualModelIDs := modelIDsForGrokTest(filteredModels)
	expectedModelIDs := []string{"grok-4.3", "Grok-Beta", "custom-grok-search"}

	if !reflect.DeepEqual(actualModelIDs, expectedModelIDs) {
		t.Fatalf("filtered model IDs = %+v, want %+v", actualModelIDs, expectedModelIDs)
	}
}

func TestListModelsCallsUpstreamAndFiltersGrokModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want %q", authorization, "Bearer test-key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.3"},{"id":"gpt-4"},{"id":" Grok-Beta "},{"id":"grok-imagine-image"},{"id":"grok-imagine-video"},{"id":"grok-video-preview"},{"id":"grok-4.3"}]}`))
	}))
	defer server.Close()

	client := newClientAt(t, server.URL)
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	actualModelIDs := modelIDsForGrokTest(models)
	expectedModelIDs := []string{"grok-4.3", "Grok-Beta"}
	if !reflect.DeepEqual(actualModelIDs, expectedModelIDs) {
		t.Fatalf("model IDs = %+v, want %+v", actualModelIDs, expectedModelIDs)
	}
}

func TestListModelsReturnsSanitizedUpstreamHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("sensitive upstream model body"))
	}))
	defer server.Close()

	client := newClientAt(t, server.URL)
	_, err := client.ListModels(context.Background())
	if err == nil || err.Error() != "upstream returned HTTP 502" {
		t.Fatalf("ListModels error = %v, want sanitized HTTP 502 error", err)
	}
}

func modelIDsForGrokTest(models []grok.Model) []string {
	modelIDs := make([]string, 0, len(models))
	for _, model := range models {
		modelIDs = append(modelIDs, model.ID)
	}
	return modelIDs
}
