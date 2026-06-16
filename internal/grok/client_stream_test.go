package grok

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grok-mcp/internal/config"
)

func TestSearchStreamMultiRoundWithSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}

		var req responsesRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !req.Stream {
			t.Fatalf("expected stream=true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			// 真实形态：added 时 item 无 action（仅 id/type/status），action 在 done 时才出现。
			`data: {"type":"response.output_item.added","item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`,
			``,
			`data: {"type":"response.output_item.added","item":{"id":"ws_2","type":"web_search_call","status":"in_progress"}}`,
			``,
			`data: {"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"capital of France"}}}`,
			``,
			`data: {"type":"response.output_item.done","item":{"id":"ws_2","type":"web_search_call","status":"completed","action":{"type":"fetch","url":"https://example.com/france"}}}`,
			``,
			`data: {"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Paris is the capital of France.","annotations":[{"type":"url_citation","url":"https://example.com/france","title":"France"}]}]}],"citations":[{"url":"https://example.com/top-level","title":"Top"}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	client := NewClient(&config.Config{
		CPABaseURL: server.URL,
		CPAAPIKey:  "test-key",
		Model:      "grok-4.3",
		Timeout:    5 * time.Second,
	})

	var rounds []SearchRound
	result, err := client.SearchStream(context.Background(), SearchRequest{
		Query:    "What is the capital of France?",
		ToolType: ToolTypeWebSearch,
	}, func(round SearchRound) {
		rounds = append(rounds, round)
	})
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}

	if len(rounds) != 2 {
		t.Fatalf("expected 2 rounds, got %d (%+v)", len(rounds), rounds)
	}
	if rounds[0].Round != 1 || rounds[0].Query != "capital of France" {
		t.Fatalf("unexpected first round: %+v", rounds[0])
	}
	if rounds[1].Round != 2 || rounds[1].URL != "https://example.com/france" {
		t.Fatalf("unexpected second round: %+v", rounds[1])
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
	if !strings.Contains(string(result.RawResponse), "response.completed") {
		t.Fatalf("expected raw response to contain completed event, got %s", string(result.RawResponse))
	}
}

func TestSearchStreamUpstreamHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway"))
	}))
	defer server.Close()

	client := NewClient(&config.Config{
		CPABaseURL: server.URL,
		CPAAPIKey:  "test-key",
		Model:      "grok-4.3",
		Timeout:    5 * time.Second,
	})

	_, err := client.SearchStream(context.Background(), SearchRequest{
		Query:    "test",
		ToolType: ToolTypeXSearch,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("expected upstream HTTP error, got %v", err)
	}
}
