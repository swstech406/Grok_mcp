package panelui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerRedirectsPanelRootToSlashPath(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panel", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusFound)
	}
	if location := responseRecorder.Header().Get("Location"); location != "/panel/" {
		t.Fatalf("Location = %q, want %q", location, "/panel/")
	}
}

func TestHandlerServesAllowedStaticAsset(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panel/app.js", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
	}
	if body := responseRecorder.Body.String(); !strings.Contains(body, "document.addEventListener") {
		t.Fatalf("expected app.js response body to look like the frontend bundle, got %q", body)
	}
}

func TestHandlerFallsBackToIndexForSpaRoutesAndUnknownAssets(t *testing.T) {
	testCases := []struct {
		name string
		path string
	}{
		{name: "panel index", path: "/panel/"},
		{name: "spa route", path: "/panel/users/42"},
		{name: "unknown asset", path: "/panel/secret.txt"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			responseRecorder := httptest.NewRecorder()

			Handler().ServeHTTP(responseRecorder, request)

			if responseRecorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
			}
			if body := responseRecorder.Body.String(); !strings.Contains(body, "<div id=\"app\"") {
				t.Fatalf("expected index.html fallback body, got %q", body)
			}
		})
	}
}

func TestHandlerRejectsNonReadMethods(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/panel/", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusMethodNotAllowed)
	}
}
