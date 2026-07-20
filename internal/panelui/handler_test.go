package panelui

import (
	"io/fs"
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
	assertPanelUICacheHeaders(t, responseRecorder)
	if body := responseRecorder.Body.String(); !strings.Contains(body, "initializeApplication();") {
		t.Fatalf("expected app.js response body to look like the frontend bundle, got %q", body)
	}
}

func TestHandlerServesConfigurationGuideModule(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panel/js/pages/configuration-guide.js", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
	}
	assertPanelUICacheHeaders(t, responseRecorder)
	if body := responseRecorder.Body.String(); !strings.Contains(body, "export function renderConfigurationGuidePage") {
		t.Fatalf("expected configuration guide module, got %q", body)
	}
}

func TestHandlerServesOperationsMetricsModule(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panel/js/pages/operations-metrics.js", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
	}
	assertPanelUICacheHeaders(t, responseRecorder)
	if body := responseRecorder.Body.String(); !strings.Contains(body, "export function renderOperationsMetricsPage") {
		t.Fatalf("expected operations metrics module, got %q", body)
	}
}

func TestHandlerServesRegistrationProofWorker(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panel/js/workers/registration-proof-worker.js", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
	}
	assertPanelUICacheHeaders(t, responseRecorder)
	if contentType := responseRecorder.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("Content-Type = %q, want JavaScript", contentType)
	}
	if body := responseRecorder.Body.String(); !strings.Contains(body, "grok-registration-pow-v1") {
		t.Fatalf("expected registration proof worker, got %q", body)
	}
}

func TestHandlerServesOperationsMetricsStylesheet(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panel/styles/pages/operations-metrics.css", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
	}
	assertPanelUICacheHeaders(t, responseRecorder)
	if contentType := responseRecorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", contentType)
	}
}

func TestPanelUIHTMLSinksAreCentralized(t *testing.T) {
	const safeHTMLModulePath = "static/js/safe-html.js"
	const innerHTMLSink = ".innerHTML"

	err := fs.WalkDir(embeddedStatic, "static", func(assetPath string, directoryEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if directoryEntry.IsDir() || !strings.HasSuffix(assetPath, ".js") {
			return nil
		}

		assetContent, readErr := embeddedStatic.ReadFile(assetPath)
		if readErr != nil {
			return readErr
		}
		content := string(assetContent)
		for _, forbiddenSink := range []string{".outerHTML", "insertAdjacentHTML(", "document.write("} {
			if strings.Contains(content, forbiddenSink) {
				t.Errorf("%s contains forbidden HTML sink %q", assetPath, forbiddenSink)
			}
		}

		innerHTMLCount := strings.Count(content, innerHTMLSink)
		if assetPath == safeHTMLModulePath {
			if innerHTMLCount != 1 {
				t.Errorf("%s innerHTML sink count = %d, want 1", assetPath, innerHTMLCount)
			}
			return nil
		}
		if innerHTMLCount != 0 {
			t.Errorf("%s bypasses renderSafeHTML with %d innerHTML sink(s)", assetPath, innerHTMLCount)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHandlerServesNestedStylesheetAsCSS(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panel/styles/foundation/tokens.css", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
	}
	assertPanelUICacheHeaders(t, responseRecorder)
	if contentType := responseRecorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", contentType)
	}
	if body := responseRecorder.Body.String(); !strings.Contains(body, "--canvas: #f2f5f1;") {
		t.Fatalf("expected tokens stylesheet response body, got %q", body)
	}
}

func TestHandlerServesSearchConcurrencySettingsFields(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panel/js/pages/settings.js", nil)
	responseRecorder := httptest.NewRecorder()

	Handler().ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
	}
	body := responseRecorder.Body.String()
	for _, expectedField := range []string{
		"mcp_global_search_concurrency",
		"mcp_user_search_concurrency",
		"operations_metrics_enabled",
		"persisted_version",
		"live_version",
		"saved_not_applied",
		"设置已保存，尚未应用",
	} {
		if !strings.Contains(body, expectedField) {
			t.Fatalf("settings module does not contain %q", expectedField)
		}
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
		{name: "unknown stylesheet", path: "/panel/styles/foundation/missing.css"},
		{name: "non css style asset", path: "/panel/styles/foundation/tokens.txt"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			responseRecorder := httptest.NewRecorder()

			Handler().ServeHTTP(responseRecorder, request)

			if responseRecorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusOK)
			}
			assertPanelUICacheHeaders(t, responseRecorder)
			if body := responseRecorder.Body.String(); !strings.Contains(body, "<div id=\"app\"") {
				t.Fatalf("expected index.html fallback body, got %q", body)
			}
		})
	}
}

func assertPanelUICacheHeaders(t *testing.T, responseRecorder *httptest.ResponseRecorder) {
	t.Helper()
	if cacheControl := responseRecorder.Header().Get("Cache-Control"); cacheControl != "no-store, max-age=0" {
		t.Fatalf("Cache-Control = %q, want no-store, max-age=0", cacheControl)
	}
	if pragma := responseRecorder.Header().Get("Pragma"); pragma != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", pragma)
	}
	if expires := responseRecorder.Header().Get("Expires"); expires != "0" {
		t.Fatalf("Expires = %q, want 0", expires)
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
