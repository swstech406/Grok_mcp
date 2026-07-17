package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/logx"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/usage"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerInstructionsDocumentSearchToolUsage(t *testing.T) {
	wantedSnippets := []string{
		webSearchToolName,
		xSearchToolName,
		listModelsToolName,
		"query is required",
		"model is optional",
		"grok keyword",
		"imagine",
		"video",
		"allowed_domains",
		"excluded_domains",
		"Do not provide allowed_domains and excluded_domains together",
		"at most 5 domains",
		"enable_image_understanding",
		"enable_image_search",
		"answer, citations, sources, and usage",
		"isError=true",
		"progressToken",
	}

	for _, wantedSnippet := range wantedSnippets {
		if !strings.Contains(ServerInstructions, wantedSnippet) {
			t.Fatalf("ServerInstructions missing %q", wantedSnippet)
		}
	}
}

func TestNewSearchToolMetadata(t *testing.T) {
	testCases := []struct {
		name        string
		title       string
		description string
	}{
		{
			name:        webSearchToolName,
			title:       webSearchToolTitle,
			description: webSearchToolDescription,
		},
		{
			name:        xSearchToolName,
			title:       xSearchToolTitle,
			description: xSearchToolDescription,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			tool := newSearchTool(testCase.name, testCase.title, testCase.description)
			if tool.Name != testCase.name {
				t.Fatalf("Name = %q, want %q", tool.Name, testCase.name)
			}
			if tool.Title != testCase.title {
				t.Fatalf("Title = %q, want %q", tool.Title, testCase.title)
			}
			if !strings.Contains(tool.Description, "query is required") {
				t.Fatalf("Description must mention query requirement; description=%q", tool.Description)
			}
			if testCase.name == webSearchToolName && !strings.Contains(tool.Description, "allowed_domains and excluded_domains are mutually exclusive") {
				t.Fatalf("web search description must mention domain filter exclusivity; description=%q", tool.Description)
			}
			if testCase.name == xSearchToolName && !strings.Contains(tool.Description, "accepts only query and model") {
				t.Fatalf("x search description must mention its smaller input schema; description=%q", tool.Description)
			}
			if tool.Annotations == nil {
				t.Fatalf("Annotations must be set")
			}
			if !tool.Annotations.ReadOnlyHint {
				t.Fatalf("ReadOnlyHint must be true")
			}
			if tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
				t.Fatalf("OpenWorldHint must be true")
			}
		})
	}
}

func TestNewListModelsToolMetadata(t *testing.T) {
	tool := newListModelsTool()
	if tool.Name != listModelsToolName {
		t.Fatalf("Name = %q, want %q", tool.Name, listModelsToolName)
	}
	if tool.Title != listModelsToolTitle {
		t.Fatalf("Title = %q, want %q", tool.Title, listModelsToolTitle)
	}
	if !strings.Contains(tool.Description, "grok keyword") || !strings.Contains(tool.Description, "imagine or video") {
		t.Fatalf("Description must mention Grok keyword filtering; description=%q", tool.Description)
	}
	if tool.Annotations == nil {
		t.Fatalf("Annotations must be set")
	}
	if !tool.Annotations.ReadOnlyHint {
		t.Fatalf("ReadOnlyHint must be true")
	}
	if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
		t.Fatalf("OpenWorldHint must be false for model listing")
	}
}

// TestWebSearchInputSchema 锁住 L1 修复：query 的 required 由 json tag（无 omitempty）自动推断，
// jsonschema tag 仅作 description，不能带 "required," 前缀污染描述文本。
func TestWebSearchInputSchema(t *testing.T) {
	schema, err := jsonschema.For[WebSearchInput](nil)
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}

	required := false
	for _, r := range schema.Required {
		if r == "query" {
			required = true
		}
	}
	if !required {
		t.Fatalf("query must be required; required=%v", schema.Required)
	}

	prop := schema.Properties["query"]
	if prop == nil {
		t.Fatalf("query property missing from schema")
	}
	if prop.Description != "Search query text" {
		t.Fatalf("query description = %q, want %q", prop.Description, "Search query text")
	}

	webSearchProperties := []string{
		"allowed_domains",
		"excluded_domains",
		"enable_image_understanding",
		"enable_image_search",
	}
	for _, propertyName := range webSearchProperties {
		if schema.Properties[propertyName] == nil {
			t.Fatalf("web search schema missing %q", propertyName)
		}
	}

}

func TestXSearchInputSchemaOmitsWebOnlyFields(t *testing.T) {
	schema, err := jsonschema.For[XSearchInput](nil)
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}

	required := false
	for _, requiredPropertyName := range schema.Required {
		if requiredPropertyName == "query" {
			required = true
		}
	}
	if !required {
		t.Fatalf("query must be required; required=%v", schema.Required)
	}

	if schema.Properties["query"] == nil {
		t.Fatalf("query property missing from x search schema")
	}
	if schema.Properties["model"] == nil {
		t.Fatalf("model property missing from x search schema")
	}

	webOnlyProperties := []string{
		"allowed_domains",
		"excluded_domains",
		"enable_image_understanding",
		"enable_image_search",
	}
	for _, propertyName := range webOnlyProperties {
		if schema.Properties[propertyName] != nil {
			t.Fatalf("x search schema must not expose %q", propertyName)
		}
	}
}

func TestFormatSearchRoundMessageSearch(t *testing.T) {
	got := formatSearchRoundMessage(grok.SearchRound{Round: 1, Query: "capital of France"})
	want := `🔍 第1轮：搜索 "capital of France"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatSearchRoundMessageFetch(t *testing.T) {
	got := formatSearchRoundMessage(grok.SearchRound{Round: 2, URL: "https://example.com/france"})
	want := "📄 第2轮：读取 https://example.com/france"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatSearchRoundMessageEmpty(t *testing.T) {
	got := formatSearchRoundMessage(grok.SearchRound{Round: 3})
	want := "🔍 第3轮：搜索中"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRunSearchReturnsStructuredOutputFromUpstream(t *testing.T) {
	var capturedRequest struct {
		Input []struct {
			Content string `json:"content"`
		} `json:"input"`
		Tools []struct {
			Type           string   `json:"type"`
			AllowedDomains []string `json:"allowed_domains"`
		} `json:"tools"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/v1/responses")
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-cpa-key" {
			t.Fatalf("Authorization = %q, want %q", authorization, "Bearer test-cpa-key")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &capturedRequest); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"structured answer","annotations":[{"type":"url_citation","url":"https://example.com/source","title":"Example Source"}]}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + completedEventForMCPTest(responseJSON) + "\n\n"))
	}))
	defer server.Close()

	toolResult, output, err := runSearch(context.Background(), nil, newMCPTestClient(t, server.URL), logx.New("mcp-test", false), grok.SearchRequest{
		Query:          "  structured query  ",
		ToolType:       grok.ToolTypeWebSearch,
		AllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatalf("runSearch returned Go error: %v", err)
	}
	if toolResult != nil {
		t.Fatalf("runSearch returned unexpected tool error: %+v", toolResult)
	}
	if output.Answer != "structured answer" {
		t.Fatalf("Answer = %q, want %q", output.Answer, "structured answer")
	}
	if len(output.Citations) != 1 || output.Citations[0] != "https://example.com/source" {
		t.Fatalf("unexpected citations: %+v", output.Citations)
	}
	if output.Usage == nil || output.Usage.TotalTokens != 3 {
		t.Fatalf("unexpected usage: %+v", output.Usage)
	}
	if len(capturedRequest.Input) != 1 || capturedRequest.Input[0].Content != "structured query" {
		t.Fatalf("expected trimmed query in upstream request, got %+v", capturedRequest.Input)
	}
	if len(capturedRequest.Tools) != 1 || capturedRequest.Tools[0].Type != "web_search" || len(capturedRequest.Tools[0].AllowedDomains) != 1 {
		t.Fatalf("unexpected upstream tools request: %+v", capturedRequest.Tools)
	}
}

func TestRunSearchMarksAuthoritativeSemanticOutcome(t *testing.T) {
	t.Run("validation error", func(t *testing.T) {
		ctx, marker := usage.WithToolOutcomeMarker(context.Background())
		toolResult, _, err := runSearch(ctx, nil, nil, logx.New("mcp-test", false), grok.SearchRequest{})
		if err != nil || toolResult == nil || !toolResult.IsError {
			t.Fatalf("unexpected validation result: toolResult=%+v err=%v", toolResult, err)
		}
		semanticSuccess, known := marker.Outcome()
		if !known || semanticSuccess {
			t.Fatalf("semantic outcome = (%t, %t), want known error", semanticSuccess, known)
		}
	})

	t.Run("successful search", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: " + completedEventForMCPTest(responseJSON) + "\n\n"))
		}))
		defer server.Close()

		ctx, marker := usage.WithToolOutcomeMarker(context.Background())
		toolResult, output, err := runSearch(ctx, nil, newMCPTestClient(t, server.URL), logx.New("mcp-test", false), grok.SearchRequest{
			Query:    "semantic outcome",
			ToolType: grok.ToolTypeWebSearch,
		})
		if err != nil || toolResult != nil || output.Answer != "ok" {
			t.Fatalf("unexpected success result: toolResult=%+v output=%+v err=%v", toolResult, output, err)
		}
		semanticSuccess, known := marker.Outcome()
		if !known || !semanticSuccess {
			t.Fatalf("semantic outcome = (%t, %t), want known success", semanticSuccess, known)
		}
	})
}

func TestRunSearchUsesRuntimeDebugState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"runtime debug answer"}]}]}`
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + completedEventForMCPTest(responseJSON) + "\n\n"))
	}))
	defer server.Close()

	configuration := &config.Config{
		CPABaseURL:       server.URL,
		CPAAPIKey:        "test-cpa-key",
		UpstreamProtocol: config.UpstreamProtocolResponses,
		Model:            "grok-4.3",
		Timeout:          5 * time.Second,
		RegistrationMode: "free",
		Debug:            false,
	}
	debugState := logx.NewDebugState(false)
	client, err := grok.NewClientWithServerSettings(configuration.ServerSettings(), debugState)
	if err != nil {
		t.Fatalf("NewClientWithServerSettings failed: %v", err)
	}
	mcpLogger := logx.NewWithDebugState("mcp-test", debugState)

	var logBuffer bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&logBuffer)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	runSearchForDebugTest := func(query string) {
		t.Helper()
		toolResult, output, err := runSearch(context.Background(), nil, client, mcpLogger, grok.SearchRequest{
			Query:    query,
			ToolType: grok.ToolTypeWebSearch,
		})
		if err != nil {
			t.Fatalf("runSearch returned Go error: %v", err)
		}
		if toolResult != nil || output.Answer != "runtime debug answer" {
			t.Fatalf("unexpected runSearch result: toolResult=%+v output=%+v", toolResult, output)
		}
	}

	runSearchForDebugTest("disabled query")
	if logBuffer.Len() != 0 {
		t.Fatalf("expected no debug logs before runtime enable, got %q", logBuffer.String())
	}

	settings := configuration.ServerSettings()
	settings.Debug = true
	if err := client.ApplyServerSettings(settings); err != nil {
		t.Fatalf("enable runtime debug: %v", err)
	}
	runSearchForDebugTest("enabled query")
	if !strings.Contains(logBuffer.String(), `[mcp-test] search start tool=web_search query="enabled query"`) {
		t.Fatalf("expected retained MCP logger to observe runtime enable, got %q", logBuffer.String())
	}

	logBuffer.Reset()
	settings.Debug = false
	if err := client.ApplyServerSettings(settings); err != nil {
		t.Fatalf("disable runtime debug: %v", err)
	}
	runSearchForDebugTest("disabled again")
	if logBuffer.Len() != 0 {
		t.Fatalf("expected retained MCP logger to observe runtime disable, got %q", logBuffer.String())
	}
}

func TestRunSearchMapsValidationAndUpstreamErrorsToMCPToolErrors(t *testing.T) {
	t.Run("missing query", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			t.Fatalf("missing-query validation should not call upstream, got %s %s", r.Method, r.URL.Path)
		}))
		defer server.Close()

		toolResult, output, err := runSearch(context.Background(), nil, newMCPTestClient(t, server.URL), logx.New("mcp-test", false), grok.SearchRequest{
			Query:    "   ",
			ToolType: grok.ToolTypeWebSearch,
		})
		if err != nil {
			t.Fatalf("runSearch returned Go error: %v", err)
		}
		if output.Answer != "" {
			t.Fatalf("expected empty output on validation error, got %+v", output)
		}
		if got := toolErrorText(t, toolResult); got != "query is required" {
			t.Fatalf("tool error text = %q, want %q", got, "query is required")
		}
	})

	t.Run("upstream HTTP status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("sensitive upstream details"))
		}))
		defer server.Close()

		toolResult, output, err := runSearch(context.Background(), nil, newMCPTestClient(t, server.URL), logx.New("mcp-test", false), grok.SearchRequest{
			Query:    "upstream failure",
			ToolType: grok.ToolTypeWebSearch,
		})
		if err != nil {
			t.Fatalf("runSearch returned Go error: %v", err)
		}
		if output.Answer != "" {
			t.Fatalf("expected empty output on upstream error, got %+v", output)
		}
		if got := toolErrorText(t, toolResult); got != "upstream returned HTTP 502" {
			t.Fatalf("tool error text = %q, want %q", got, "upstream returned HTTP 502")
		}
		if strings.Contains(toolErrorText(t, toolResult), "sensitive upstream details") {
			t.Fatalf("tool error leaked upstream body: %q", toolErrorText(t, toolResult))
		}
	})
}

func TestRunSearchSendsXSearchToolTypeWithoutWebOnlyFields(t *testing.T) {
	var capturedRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &capturedRequest); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"x answer"}]}]}`
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + completedEventForMCPTest(responseJSON) + "\n\n"))
	}))
	defer server.Close()

	toolResult, output, err := runSearch(context.Background(), nil, newMCPTestClient(t, server.URL), logx.New("mcp-test", false), grok.SearchRequest{
		Query:                    "x query",
		ToolType:                 grok.ToolTypeXSearch,
		AllowedDomains:           []string{"ignored.example"},
		EnableImageUnderstanding: boolPointerForMCPTest(true),
	})
	if err != nil {
		t.Fatalf("runSearch returned Go error: %v", err)
	}
	if toolResult != nil || output.Answer != "x answer" {
		t.Fatalf("unexpected runSearch result: toolResult=%+v output=%+v", toolResult, output)
	}

	tools, ok := capturedRequest["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("unexpected tools payload: %+v", capturedRequest["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected tool payload: %+v", tools[0])
	}
	if tool["type"] != "x_search" {
		t.Fatalf("tool type = %v, want x_search", tool["type"])
	}
	for _, webOnlyField := range []string{"allowed_domains", "excluded_domains", "enable_image_understanding", "enable_image_search"} {
		if _, exists := tool[webOnlyField]; exists {
			t.Fatalf("x_search request must not include %q: %+v", webOnlyField, tool)
		}
	}
}

func TestRunListModelsReturnsOnlyFilteredGrokModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/v1/models")
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-cpa-key" {
			t.Fatalf("Authorization = %q, want %q", authorization, "Bearer test-cpa-key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.3"},{"id":"gpt-4"},{"id":" Grok-Beta "},{"id":"grok-imagine-image"},{"id":"grok-imagine-video"},{"id":"grok-video-preview"},{"id":"grok-4.3"}]}`))
	}))
	defer server.Close()

	toolResult, output, err := runListModels(context.Background(), newMCPTestClient(t, server.URL), logx.New("mcp-test", false))
	if err != nil {
		t.Fatalf("runListModels returned Go error: %v", err)
	}
	if toolResult != nil {
		t.Fatalf("runListModels returned unexpected tool error: %+v", toolResult)
	}

	modelIDs := make([]string, 0, len(output.Models))
	for _, model := range output.Models {
		modelIDs = append(modelIDs, model.ID)
	}
	wantedModelIDs := []string{"grok-4.3", "Grok-Beta"}
	if len(modelIDs) != len(wantedModelIDs) {
		t.Fatalf("model IDs = %+v, want %+v", modelIDs, wantedModelIDs)
	}
	for index, wantedModelID := range wantedModelIDs {
		if modelIDs[index] != wantedModelID {
			t.Fatalf("model IDs = %+v, want %+v", modelIDs, wantedModelIDs)
		}
	}
}

func newMCPTestClient(t *testing.T, baseURL string) *grok.Client {
	t.Helper()
	configuration := &config.Config{
		CPABaseURL:       baseURL,
		CPAAPIKey:        "test-cpa-key",
		UpstreamProtocol: config.UpstreamProtocolResponses,
		Model:            "grok-4.3",
		Timeout:          5 * time.Second,
		RegistrationMode: "free",
	}
	client, err := grok.NewClientWithServerSettings(configuration.ServerSettings(), nil)
	if err != nil {
		t.Fatalf("NewClientWithServerSettings failed: %v", err)
	}
	return client
}

func completedEventForMCPTest(responseJSON string) string {
	return `{"type":"response.completed","response":` + strings.TrimSpace(responseJSON) + `}`
}

func toolErrorText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil {
		t.Fatal("expected MCP tool error result, got nil")
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true, got %+v", result)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected exactly one error content item, got %+v", result.Content)
	}
	textContent, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text error content, got %T", result.Content[0])
	}
	return textContent.Text
}

func boolPointerForMCPTest(value bool) *bool {
	return &value
}
