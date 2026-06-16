package grok

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/config"
)

func TestBuildSearchResultWithAnnotationsAndTopLevelCitations(t *testing.T) {
	raw := []byte(`{
		"output": [{
			"type": "message",
			"role": "assistant",
			"content": [{
				"type": "output_text",
				"text": "Paris is the capital of France.",
				"annotations": [{
					"type": "url_citation",
					"url": "https://example.com/france",
					"title": "France"
				}]
			}]
		}],
		"citations": [{"url": "https://example.com/top-level", "title": "Top"}],
		"usage": {"input_tokens": 10, "output_tokens": 20, "total_tokens": 30}
	}`)

	var parsed responsesResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	result, err := buildSearchResult(parsed, raw)
	if err != nil {
		t.Fatalf("buildSearchResult failed: %v", err)
	}

	if result.Answer != "Paris is the capital of France." {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if len(result.Citations) != 2 {
		t.Fatalf("expected 2 citations, got %d (%v)", len(result.Citations), result.Citations)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d (%+v)", len(result.Sources), result.Sources)
	}
	if result.Sources[0].URL != "https://example.com/france" || result.Sources[0].Title != "France" {
		t.Fatalf("unexpected first source: %+v", result.Sources[0])
	}
	if result.Sources[1].URL != "https://example.com/top-level" || result.Sources[1].Title != "Top" {
		t.Fatalf("unexpected second source: %+v", result.Sources[1])
	}
	if result.Usage == nil || result.Usage.TotalTokens != 30 {
		t.Fatalf("unexpected usage: %+v", result.Usage)
	}
}

func TestBuildSearchResultCitationsAsStringArray(t *testing.T) {
	raw := []byte(`{
		"output": [{"role": "assistant", "content": [{"type": "output_text", "text": "answer"}]}],
		"citations": ["https://a.example", "https://b.example"]
	}`)

	var parsed responsesResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	result, err := buildSearchResult(parsed, raw)
	if err != nil {
		t.Fatalf("buildSearchResult failed: %v", err)
	}
	if len(result.Citations) != 2 {
		t.Fatalf("expected 2 string-array citations, got %d (%v)", len(result.Citations), result.Citations)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d (%+v)", len(result.Sources), result.Sources)
	}
}

func TestBuildSearchResultEmptyAnswer(t *testing.T) {
	raw := []byte(`{"output": [{"role": "assistant", "content": [{"type": "output_text", "text": "   "}]}]}`)
	var parsed responsesResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	_, err := buildSearchResult(parsed, raw)
	if err == nil || !strings.Contains(err.Error(), "did not contain answer text") {
		t.Fatalf("expected empty answer error, got %v", err)
	}
}

func TestValidateSearchRequestErrors(t *testing.T) {
	if err := validateSearchRequest(SearchRequest{Query: " ", ToolType: ToolTypeWebSearch}); err == nil || !strings.Contains(err.Error(), "query must not be empty") {
		t.Fatalf("expected empty query error, got %v", err)
	}

	if err := validateSearchRequest(SearchRequest{Query: "test", ToolType: ToolType("bad")}); err == nil || !strings.Contains(err.Error(), "unsupported tool type") {
		t.Fatalf("expected unsupported tool type error, got %v", err)
	}

	if err := validateSearchRequest(SearchRequest{
		Query:           "test",
		ToolType:        ToolTypeWebSearch,
		AllowedDomains:  []string{"a.com"},
		ExcludedDomains: []string{"b.com"},
	}); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("expected domain conflict error, got %v", err)
	}

	if err := validateSearchRequest(SearchRequest{
		Query:          "test",
		ToolType:       ToolTypeWebSearch,
		AllowedDomains: []string{"a.com", "b.com", "c.com", "d.com", "e.com", "f.com"},
	}); err == nil || !strings.Contains(err.Error(), "allowed_domains supports at most 5 entries") {
		t.Fatalf("expected allowed_domains limit error, got %v", err)
	}

	if err := validateSearchRequest(SearchRequest{
		Query:           "test",
		ToolType:        ToolTypeWebSearch,
		ExcludedDomains: []string{"a.com", "b.com", "c.com", "d.com", "e.com", "f.com"},
	}); err == nil || !strings.Contains(err.Error(), "excluded_domains supports at most 5 entries") {
		t.Fatalf("expected excluded_domains limit error, got %v", err)
	}
}

func TestBuildSearchRequestBodyWebSearchTopLevelDomains(t *testing.T) {
	client := NewClient(&config.Config{
		CPABaseURL: "http://127.0.0.1:8317",
		CPAAPIKey:  "test-key",
		Model:      "grok-4.3",
		Timeout:    5 * time.Second,
	})

	_, body, err := client.buildSearchRequestBody(SearchRequest{
		Query:          "test",
		ToolType:       ToolTypeWebSearch,
		AllowedDomains: []string{"a.com", "b.com"},
	}, true)
	if err != nil {
		t.Fatalf("buildSearchRequestBody failed: %v", err)
	}

	if rawStr := string(body); strings.Contains(rawStr, `"filters"`) {
		t.Fatalf("request must not contain nested filters; body=%s", rawStr)
	}

	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if !req.Stream {
		t.Fatalf("expected stream=true")
	}
	if len(req.Tools) != 1 || len(req.Tools[0].AllowedDomains) != 2 {
		t.Fatalf("expected top-level allowed_domains on tool, got %+v", req.Tools)
	}
}

func TestBuildSearchRequestBodyXSearchNoWebFields(t *testing.T) {
	client := NewClient(&config.Config{
		CPABaseURL: "http://127.0.0.1:8317",
		CPAAPIKey:  "test-key",
		Model:      "grok-4.3",
		Timeout:    5 * time.Second,
	})

	_, body, err := client.buildSearchRequestBody(SearchRequest{
		Query:          "test",
		ToolType:       ToolTypeXSearch,
		AllowedDomains: []string{"a.com"},
	}, true)
	if err != nil {
		t.Fatalf("buildSearchRequestBody failed: %v", err)
	}

	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Type != "x_search" {
		t.Fatalf("unexpected tools: %+v", req.Tools)
	}
	if len(req.Tools[0].AllowedDomains) != 0 {
		t.Fatalf("x_search must not include web-only fields, got %+v", req.Tools[0])
	}
}
