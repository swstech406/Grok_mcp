package grok_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
)

// sse builds valid SSE text from complete event payloads (each followed by a blank line).
func sse(payloads ...string) string {
	var b strings.Builder
	for _, p := range payloads {
		b.WriteString("data: ")
		b.WriteString(p)
		b.WriteString("\n\n")
	}
	return b.String()
}

func newClientAt(t *testing.T, baseURL string) *grok.Client {
	t.Helper()
	configuration := &config.Config{
		CPABaseURL:       baseURL,
		CPAAPIKey:        "test-key",
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

func newNoCallServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request during validation: %s %s", r.Method, r.URL.Path)
	}))
}

// newSSEServer 启动一个伪 CPA，向请求回写给定的 SSE 文本。
func newSSEServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
}

type capturedRequest struct {
	Model string `json:"model"`
	Input []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"input"`
	Tools []struct {
		Type                     string   `json:"type"`
		AllowedDomains           []string `json:"allowed_domains,omitempty"`
		ExcludedDomains          []string `json:"excluded_domains,omitempty"`
		EnableImageUnderstanding *bool    `json:"enable_image_understanding,omitempty"`
		EnableImageSearch        *bool    `json:"enable_image_search,omitempty"`
	} `json:"tools"`
	Stream bool `json:"stream"`
}

func TestSearchStreamValidateSearchRequestErrors(t *testing.T) {
	server := newNoCallServer(t)
	defer server.Close()
	client := newClientAt(t, server.URL)
	ctx := context.Background()

	cases := []struct {
		name string
		req  grok.SearchRequest
		want string
	}{
		{
			name: "empty query",
			req:  grok.SearchRequest{Query: " ", ToolType: grok.ToolTypeWebSearch},
			want: "query must not be empty",
		},
		{
			name: "unsupported tool type",
			req:  grok.SearchRequest{Query: "test", ToolType: grok.ToolType("bad")},
			want: "unsupported tool type",
		},
		{
			name: "domain conflict",
			req: grok.SearchRequest{
				Query:           "test",
				ToolType:        grok.ToolTypeWebSearch,
				AllowedDomains:  []string{"a.com"},
				ExcludedDomains: []string{"b.com"},
			},
			want: "cannot be used together",
		},
		{
			name: "allowed_domains limit",
			req: grok.SearchRequest{
				Query:          "test",
				ToolType:       grok.ToolTypeWebSearch,
				AllowedDomains: []string{"a.com", "b.com", "c.com", "d.com", "e.com", "f.com"},
			},
			want: "allowed_domains supports at most 5 entries",
		},
		{
			name: "excluded_domains limit",
			req: grok.SearchRequest{
				Query:           "test",
				ToolType:        grok.ToolTypeWebSearch,
				ExcludedDomains: []string{"a.com", "b.com", "c.com", "d.com", "e.com", "f.com"},
			},
			want: "excluded_domains supports at most 5 entries",
		},
		{
			name: "domain filter rejects scheme",
			req: grok.SearchRequest{
				Query:          "test",
				ToolType:       grok.ToolTypeWebSearch,
				AllowedDomains: []string{"https://example.com"},
			},
			want: "scheme is not allowed",
		},
		{
			name: "domain filter rejects localhost",
			req: grok.SearchRequest{
				Query:           "test",
				ToolType:        grok.ToolTypeWebSearch,
				ExcludedDomains: []string{"localhost"},
			},
			want: "local hostnames are not allowed",
		},
		{
			name: "domain filter rejects ip literals",
			req: grok.SearchRequest{
				Query:          "test",
				ToolType:       grok.ToolTypeWebSearch,
				AllowedDomains: []string{"127.0.0.1"},
			},
			want: "IP literals are not allowed",
		},
		{
			name: "unsupported model",
			req: grok.SearchRequest{
				Query:    "test",
				ToolType: grok.ToolTypeWebSearch,
				Model:    "gpt-4",
			},
			want: "unsupported model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.SearchStream(ctx, tc.req, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestSearchStreamWebSearchTopLevelDomains(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		rawBody = string(body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse(completedEvent(`{"output":[{"role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))))
	}))
	defer server.Close()

	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:          "test",
		ToolType:       grok.ToolTypeWebSearch,
		AllowedDomains: []string{" A.COM ", "b.com"},
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}

	if strings.Contains(rawBody, `"filters"`) {
		t.Fatalf("request must not contain nested filters; body=%s", rawBody)
	}

	var req capturedRequest
	if err := json.Unmarshal([]byte(rawBody), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if !req.Stream {
		t.Fatalf("expected stream=true")
	}
	if len(req.Tools) != 1 || len(req.Tools[0].AllowedDomains) != 2 {
		t.Fatalf("expected top-level allowed_domains on tool, got %+v", req.Tools)
	}
	if req.Tools[0].AllowedDomains[0] != "a.com" {
		t.Fatalf("expected domain filters to be normalized, got %+v", req.Tools[0].AllowedDomains)
	}
}

func TestSearchStreamXSearchNoWebFields(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		rawBody = string(body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse(completedEvent(`{"output":[{"role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))))
	}))
	defer server.Close()

	client := newClientAt(t, server.URL)
	enableImageUnderstanding := true
	enableImageSearch := true
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:                    "test",
		ToolType:                 grok.ToolTypeXSearch,
		AllowedDomains:           []string{"a.com", "b.com", "c.com", "d.com", "e.com", "f.com"},
		ExcludedDomains:          []string{"excluded.example"},
		EnableImageUnderstanding: &enableImageUnderstanding,
		EnableImageSearch:        &enableImageSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}

	var req capturedRequest
	if err := json.Unmarshal([]byte(rawBody), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Type != "x_search" {
		t.Fatalf("unexpected tools: %+v", req.Tools)
	}
	if len(req.Tools[0].AllowedDomains) != 0 || len(req.Tools[0].ExcludedDomains) != 0 {
		t.Fatalf("x_search must not include web-only fields, got %+v", req.Tools[0])
	}
	if req.Tools[0].EnableImageUnderstanding != nil || req.Tools[0].EnableImageSearch != nil {
		t.Fatalf("x_search must not include image fields, got %+v", req.Tools[0])
	}
}

func TestSearchStreamAnnotationsAndTopLevelCitations(t *testing.T) {
	responseJSON := `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Paris is the capital of France.","annotations":[{"type":"url_citation","url":"https://example.com/france","title":"France"}]}]}],"citations":[{"url":"https://example.com/top-level","title":"Top"}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`

	server := newSSEServer(t, sse(completedEvent(responseJSON)))
	defer server.Close()

	client := newClientAt(t, server.URL)
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
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

func TestSearchStreamCitationsAsStringArray(t *testing.T) {
	responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"citations":["https://a.example","https://b.example"]}`

	server := newSSEServer(t, sse(completedEvent(responseJSON)))
	defer server.Close()

	client := newClientAt(t, server.URL)
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if len(result.Citations) != 2 {
		t.Fatalf("expected 2 string-array citations, got %d (%v)", len(result.Citations), result.Citations)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d (%+v)", len(result.Sources), result.Sources)
	}
}

func TestSearchStreamEmptyAnswer(t *testing.T) {
	responseJSON := `{"output": [{"role": "assistant", "content": [{"type": "output_text", "text": "   "}]}]}`

	server := newSSEServer(t, sse(completedEvent(responseJSON)))
	defer server.Close()

	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "did not contain answer text") {
		t.Fatalf("expected empty answer error, got %v", err)
	}
}

func TestSearchStreamMultilineSSEDataPayloadJoin(t *testing.T) {
	// 单条事件：完整 JSON 放在一行 data: 上（避免依赖重复键解析）。
	itemDone := `{"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"capital of France"}}}`
	stream := sse(itemDone, completedEvent(`{"output":[{"role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`))

	server := newSSEServer(t, stream)
	defer server.Close()

	var rounds []grok.SearchRound
	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, func(round grok.SearchRound) {
		rounds = append(rounds, round)
	})
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if len(rounds) != 1 {
		t.Fatalf("expected 1 round, got %d (%+v)", len(rounds), rounds)
	}
	if rounds[0].Round != 1 || rounds[0].Query != "capital of France" {
		t.Fatalf("unexpected round: %+v", rounds[0])
	}
}

func TestSearchStreamSSEDoneMarker(t *testing.T) {
	responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"after done"}]}]}`
	stream := sse("[DONE]", completedEvent(responseJSON))

	server := newSSEServer(t, stream)
	defer server.Close()

	client := newClientAt(t, server.URL)
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if result.Answer != "after done" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
}

func TestSearchStreamSSETrailingDataWithoutBlankLine(t *testing.T) {
	responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"trailing ok"}]}]}`
	stream := "data: " + completedEvent(responseJSON)

	server := newSSEServer(t, stream)
	defer server.Close()

	client := newClientAt(t, server.URL)
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if result.Answer != "trailing ok" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
}

func TestSearchStreamMultiRoundWithSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}

		var req capturedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !req.Stream {
			t.Fatalf("expected stream=true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"response.output_item.added","item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`,
			``,
			`data: {"type":"response.output_item.added","item":{"id":"ws_2","type":"web_search_call","status":"in_progress"}}`,
			``,
			`data: {"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"capital of France"}}}`,
			``,
			`data: {"type":"response.output_item.done","item":{"id":"ws_2","type":"web_search_call","status":"completed","action":{"type":"fetch","url":"https://example.com/france"}}}`,
			``,
			"data: " + completedEvent(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Paris is the capital of France.","annotations":[{"type":"url_citation","url":"https://example.com/france","title":"France"}]}]}],"citations":[{"url":"https://example.com/top-level","title":"Top"}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`),
			``,
		}, "\n")))
	}))
	defer server.Close()

	client := newClientAt(t, server.URL)

	var rounds []grok.SearchRound
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "What is the capital of France?",
		ToolType: grok.ToolTypeWebSearch,
	}, func(round grok.SearchRound) {
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
	cases := []struct {
		name string
		code int
	}{
		{name: "bad_request", code: http.StatusBadRequest},
		{name: "unauthorized", code: http.StatusUnauthorized},
		{name: "too_many_requests", code: http.StatusTooManyRequests},
		{name: "bad_gateway", code: http.StatusBadGateway},
		{name: "service_unavailable", code: http.StatusServiceUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte("upstream error body"))
			}))
			defer server.Close()

			client := newClientAt(t, server.URL)
			_, err := client.SearchStream(context.Background(), grok.SearchRequest{
				Query:    "test",
				ToolType: grok.ToolTypeWebSearch,
			}, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			want := fmt.Sprintf("HTTP %d", tc.code)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("expected error containing %q, got %v", want, err)
			}
			if strings.Contains(err.Error(), "upstream error body") {
				t.Fatalf("must not leak upstream body in error: %v", err)
			}
		})
	}
}

func TestSearchStreamRejectsHTTPRedirect(t *testing.T) {
	var hits int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/v1/responses" {
			http.Redirect(w, r, srv.URL+"/v1/responses/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newClientAt(t, srv.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("expected redirect rejected as HTTP 302, got %v (hits=%d)", err, hits)
	}
	if hits != 1 {
		t.Fatalf("redirect must not be followed automatically, got %d requests", hits)
	}
}

func TestSearchStreamSSEErrorEvent(t *testing.T) {
	stream := sse(
		`{"type":"error","error":{"message":"rate limited"}}`,
		completedEvent(`{"output":[{"role":"assistant","content":[{"type":"output_text","text":"ignored"}]}]}`),
	)
	server := newSSEServer(t, stream)
	defer server.Close()
	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "upstream stream error") {
		t.Fatalf("expected stream error, got %v", err)
	}
}

func TestSearchStreamMissingCompleted(t *testing.T) {
	stream := sse(`{"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}}`)
	server := newSSEServer(t, stream)
	defer server.Close()
	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "without response.completed") {
		t.Fatalf("expected missing completed error, got %v", err)
	}
}

func TestSearchStreamInvalidCompletedJSON(t *testing.T) {
	stream := sse(`{"type":"response.completed","response":not-json}`)
	server := newSSEServer(t, stream)
	defer server.Close()
	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "decode stream event") {
		t.Fatalf("expected decode stream event error, got %v", err)
	}
}

func TestSearchStreamWebSearchImageFlagsInRequest(t *testing.T) {
	imgUnderstand, imgSearch := true, false
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rawBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse(completedEvent(`{"output":[{"role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))))
	}))
	defer server.Close()

	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:                    "test",
		ToolType:                 grok.ToolTypeWebSearch,
		EnableImageUnderstanding: &imgUnderstand,
		EnableImageSearch:        &imgSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream: %v", err)
	}
	var req capturedRequest
	if err := json.Unmarshal([]byte(rawBody), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("tools: %+v", req.Tools)
	}
	if req.Tools[0].EnableImageUnderstanding == nil || !*req.Tools[0].EnableImageUnderstanding {
		t.Fatalf("enable_image_understanding: %+v", req.Tools[0])
	}
	if req.Tools[0].EnableImageSearch == nil || *req.Tools[0].EnableImageSearch {
		t.Fatalf("enable_image_search: %+v", req.Tools[0])
	}
}

func TestSearchStreamXSearchAnswerAndCitations(t *testing.T) {
	responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"x answer"}]}],"citations":["https://x.example/post"]}`
	server := newSSEServer(t, sse(completedEvent(responseJSON)))
	defer server.Close()
	client := newClientAt(t, server.URL)
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeXSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream: %v", err)
	}
	if result.Answer != "x answer" {
		t.Fatalf("answer %q", result.Answer)
	}
	if len(result.Citations) != 1 || result.Citations[0] != "https://x.example/post" {
		t.Fatalf("citations %v", result.Citations)
	}
}

func TestSearchStreamSSEIgnoresCommentsAndEventLines(t *testing.T) {
	stream := strings.Join([]string{
		": keepalive comment",
		"event: response.completed",
		"data: " + completedEvent(`{"output":[{"role":"assistant","content":[{"type":"output_text","text":"comment ok"}]}]}`),
		"",
	}, "\n")

	server := newSSEServer(t, stream)
	defer server.Close()

	client := newClientAt(t, server.URL)
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if result.Answer != "comment ok" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
}

func TestSearchStreamDeduplicatesCitationsAndIgnoresEmptyCitationItems(t *testing.T) {
	responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[{"type":"url_citation","url":"https://example.com/a","title":"A"},{"type":"url_citation","url":"https://example.com/a","title":"Duplicate"},{"type":"file_citation","url":"https://example.com/ignored","title":"Ignored"}]}]}],"citations":[{"url":"https://example.com/a","title":"Top Duplicate"},{"url":"   ","title":"Empty"},null,"https://example.com/b"]}`

	server := newSSEServer(t, sse(completedEvent(responseJSON)))
	defer server.Close()

	client := newClientAt(t, server.URL)
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if len(result.Citations) != 2 {
		t.Fatalf("expected 2 unique citations, got %d (%v)", len(result.Citations), result.Citations)
	}
	if result.Citations[0] != "https://example.com/a" || result.Citations[1] != "https://example.com/b" {
		t.Fatalf("unexpected citation order: %v", result.Citations)
	}
	if result.Sources[0].Title != "A" {
		t.Fatalf("expected first source title from first annotation, got %+v", result.Sources[0])
	}
}

func TestSearchStreamMultipleOutputTextBlocksJoinedByNewline(t *testing.T) {
	responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":" first paragraph "},{"type":"output_text","text":"second paragraph"},{"type":"output_text","text":"   "}]}]}`

	server := newSSEServer(t, sse(completedEvent(responseJSON)))
	defer server.Close()

	client := newClientAt(t, server.URL)
	result, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if result.Answer != "first paragraph\nsecond paragraph" {
		t.Fatalf("unexpected joined answer: %q", result.Answer)
	}
}

func TestSearchStreamMalformedUsageReturnsDecodeError(t *testing.T) {
	responseJSON := `{"output":[{"role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":"not an object"}`

	server := newSSEServer(t, sse(completedEvent(responseJSON)))
	defer server.Close()

	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeWebSearch,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "decode usage") {
		t.Fatalf("expected usage decode error, got %v", err)
	}
}

func TestSearchStreamXSearchSSEReportsRound(t *testing.T) {
	stream := sse(
		`{"type":"response.output_item.done","item":{"type":"x_search_call","action":{"query":"current X posts"}}}`,
		`{"type":"response.output_item.done","item":{"type":"message","content":[]}}`,
		completedEvent(`{"output":[{"role":"assistant","content":[{"type":"output_text","text":"round ok"}]}]}`),
	)

	server := newSSEServer(t, stream)
	defer server.Close()

	var rounds []grok.SearchRound
	client := newClientAt(t, server.URL)
	_, err := client.SearchStream(context.Background(), grok.SearchRequest{
		Query:    "test",
		ToolType: grok.ToolTypeXSearch,
	}, func(round grok.SearchRound) {
		rounds = append(rounds, round)
	})
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if len(rounds) != 1 {
		t.Fatalf("expected one X search round to be reported, got %+v", rounds)
	}
	if rounds[0].Round != 1 || rounds[0].Query != "current X posts" {
		t.Fatalf("unexpected X search round: %+v", rounds[0])
	}
}

func completedEvent(responseJSON string) string {
	return `{"type":"response.completed","response":` + strings.TrimSpace(responseJSON) + `}`
}
