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

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
)

func TestBuildChatCompletionsRequestBodyMapsSearchSources(t *testing.T) {
	_, body, err := buildChatCompletionsRequestBody(SearchRequest{
		Query:           "latest news",
		ToolType:        ToolTypeWebSearch,
		AllowedDomains:  []string{"example.com"},
		ExcludedDomains: nil,
	}, "grok-4.3")
	if err != nil {
		t.Fatalf("build chat completions request: %v", err)
	}

	var request chatCompletionsRequest
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode chat completions request: %v", err)
	}
	if len(request.Messages) != 1 || request.Messages[0].Content != "latest news" {
		t.Fatalf("unexpected messages: %+v", request.Messages)
	}
	if !request.Stream || !request.StreamOptions.IncludeUsage {
		t.Fatalf("streaming usage must be enabled: %+v", request)
	}
	if len(request.SearchParameters.Sources) != 1 {
		t.Fatalf("expected one search source: %+v", request.SearchParameters)
	}
	source := request.SearchParameters.Sources[0]
	if source.Type != "web" || len(source.AllowedWebsites) != 1 || source.AllowedWebsites[0] != "example.com" {
		t.Fatalf("unexpected web source: %+v", source)
	}

	_, xBody, err := buildChatCompletionsRequestBody(SearchRequest{
		Query:    "recent posts",
		ToolType: ToolTypeXSearch,
	}, "grok-4.3")
	if err != nil {
		t.Fatalf("build X chat completions request: %v", err)
	}
	if err := json.Unmarshal(xBody, &request); err != nil {
		t.Fatalf("decode X chat completions request: %v", err)
	}
	if request.SearchParameters.Sources[0].Type != "x" {
		t.Fatalf("expected X search source, got %+v", request.SearchParameters.Sources[0])
	}
}

func TestParseChatCompletionsResponseAggregatesTextCitationsAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello "}}]}`,
		"",
		`data: {"choices":[{"delta":{"content":"world"}}],"citations":[{"url":"https://example.com","title":"Example"}]}`,
		"",
		`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	result, err := parseChatCompletionsResponse(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parse chat completions response: %v", err)
	}
	if result.Answer != "Hello world" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if len(result.Sources) != 1 || result.Sources[0].URL != "https://example.com" {
		t.Fatalf("unexpected sources: %+v", result.Sources)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected usage: %+v", result.Usage)
	}
}

func TestParseChatCompletionsResponseSupportsCPAExtensions(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"search_1","type":"web_search_call","action":{"query":"Go release notes"}}}`,
		"",
		`data: {"type":"web_search_call.completed","id":"search_1","action":{"query":"Go release notes"}}`,
		"",
		`data: {"type":"provider.status","status":"searching"}`,
		"",
		`data: {"choices":[{"delta":{"content":"Verified answer"}}]}`,
		"",
		`data: {"choices":[{"delta":{"annotations":[{"type":"url_citation","url_citation":{"url":"https://go.dev/doc/go1.25","title":"Go 1.25 Release Notes"}}]}}]}`,
		"",
		`data: {"choices":[],"sources":[{"source_url":"https://go.dev/blog/go1.25","name":"Go Blog"}],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var searchRounds []SearchRound
	result, err := parseChatCompletionsResponse(strings.NewReader(stream), func(searchRound SearchRound) {
		searchRounds = append(searchRounds, searchRound)
	}, nil)
	if err != nil {
		t.Fatalf("parse extended chat completions response: %v", err)
	}
	if result.Answer != "Verified answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if len(searchRounds) != 1 || searchRounds[0].Query != "Go release notes" {
		t.Fatalf("expected one deduplicated search round, got %+v", searchRounds)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("expected nested annotation and source aliases, got %+v", result.Sources)
	}
	if result.Sources[0].URL != "https://go.dev/doc/go1.25" || result.Sources[0].Title != "Go 1.25 Release Notes" {
		t.Fatalf("unexpected nested citation: %+v", result.Sources[0])
	}
	if result.Sources[1].URL != "https://go.dev/blog/go1.25" || result.Sources[1].Title != "Go Blog" {
		t.Fatalf("unexpected source alias: %+v", result.Sources[1])
	}
	if result.Usage == nil || result.Usage.TotalTokens != 13 {
		t.Fatalf("unexpected usage: %+v", result.Usage)
	}
}

func TestParseChatCompletionsResponseEmitsProgressBeforeStreamCompletes(t *testing.T) {
	streamReader, streamWriter := io.Pipe()
	defer streamWriter.Close()

	type parseResult struct {
		result *SearchResult
		err    error
	}
	searchRoundReceived := make(chan SearchRound, 1)
	parseCompleted := make(chan parseResult, 1)
	go func() {
		result, err := parseChatCompletionsResponse(streamReader, func(searchRound SearchRound) {
			searchRoundReceived <- searchRound
		}, nil)
		parseCompleted <- parseResult{result: result, err: err}
	}()

	firstEvent := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"search_1","type":"web_search_call","action":{"query":"Go release notes"}}}`,
		"",
	}, "\n") + "\n"
	if _, err := io.WriteString(streamWriter, firstEvent); err != nil {
		t.Fatalf("write first stream event: %v", err)
	}

	select {
	case searchRound := <-searchRoundReceived:
		if searchRound.Query != "Go release notes" {
			t.Fatalf("unexpected search round: %+v", searchRound)
		}
	case completed := <-parseCompleted:
		t.Fatalf("parser completed before upstream stream ended: result=%+v err=%v", completed.result, completed.err)
	case <-time.After(time.Second):
		t.Fatal("search progress was not emitted while upstream stream remained open")
	}

	remainingEvents := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Verified answer"}}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	if _, err := io.WriteString(streamWriter, remainingEvents); err != nil {
		t.Fatalf("write remaining stream events: %v", err)
	}
	if err := streamWriter.Close(); err != nil {
		t.Fatalf("close stream writer: %v", err)
	}

	select {
	case completed := <-parseCompleted:
		if completed.err != nil {
			t.Fatalf("parse chat completions response: %v", completed.err)
		}
		if completed.result.Answer != "Verified answer" {
			t.Fatalf("unexpected answer: %q", completed.result.Answer)
		}
	case <-time.After(time.Second):
		t.Fatal("parser did not complete after upstream stream closed")
	}
}

func TestParseChatCompletionsResponseDoesNotMisclassifyJSONTextAsSSE(t *testing.T) {
	responseBody := `{"choices":[{"message":{"content":"A literal data: prefix is ordinary text."}}]}`
	result, err := parseChatCompletionsResponse(strings.NewReader(responseBody), nil, nil)
	if err != nil {
		t.Fatalf("parse non-streaming response: %v", err)
	}
	if result.Answer != "A literal data: prefix is ordinary text." {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
}

func TestParseChatCompletionsResponsePreservesRawSSEBody(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"first answer\"}}]}\n\ndata: [DONE]\n\n"
	result, err := parseChatCompletionsResponse(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parse chat completions SSE response: %v", err)
	}
	if string(result.RawResponse) != stream {
		t.Fatalf("raw response changed:\n got: %q\nwant: %q", result.RawResponse, stream)
	}

	preservedRawResponse := string(result.RawResponse)
	secondStream := "data: {\"choices\":[{\"delta\":{\"content\":\"second answer\"}}]}\n\ndata: [DONE]\n\n"
	if _, err := parseChatCompletionsResponse(strings.NewReader(secondStream), nil, nil); err != nil {
		t.Fatalf("parse second chat completions SSE response: %v", err)
	}
	if string(result.RawResponse) != preservedRawResponse {
		t.Fatalf("later parse mutated prior raw response: %q", result.RawResponse)
	}
}

func TestParseChatCompletionsResponseAcceptsLargeNonSSEBody(t *testing.T) {
	responseBody := `{"padding":"` + strings.Repeat("a", maxSSEEventBytes+1) + `","choices":[{"message":{"content":"answer"}}]}`
	result, err := parseChatCompletionsResponse(strings.NewReader(responseBody), nil, nil)
	if err != nil {
		t.Fatalf("parse large non-SSE response: %v", err)
	}
	if result.Answer != "answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if string(result.RawResponse) != responseBody {
		t.Fatal("large non-SSE raw response was not preserved")
	}
}

func TestParseChatCompletionsResponseAllowsTrailingWhitespace(t *testing.T) {
	responseBody := " \n" + `{"choices":[{"message":{"content":"answer"}}]}` + "\n\t"
	result, err := parseChatCompletionsResponse(strings.NewReader(responseBody), nil, nil)
	if err != nil {
		t.Fatalf("parse response with trailing whitespace: %v", err)
	}
	if result.Answer != "answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if string(result.RawResponse) != responseBody {
		t.Fatalf("raw response changed: got %q, want %q", result.RawResponse, responseBody)
	}
}

func TestParseChatCompletionsResponseRejectsTrailingJSONValue(t *testing.T) {
	responseBody := `{"choices":[{"message":{"content":"answer"}}]} {"extra":true}`
	_, err := parseChatCompletionsResponse(strings.NewReader(responseBody), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected trailing JSON value") {
		t.Fatalf("expected trailing JSON value error, got %v", err)
	}
}

func TestParseSearchStreamRetainsCompletedPayloadAcrossLaterEvents(t *testing.T) {
	completedPayload := `{"type":"response.completed","response":{"output":[{"content":[{"text":"answer"}]}]}}`
	laterPayload := `{"type":"provider.status","padding":"` + strings.Repeat("x", len(completedPayload)) + `"}`
	stream := "data: " + completedPayload + "\n\ndata: " + laterPayload + "\n\n"

	result, err := parseSearchStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parse Responses stream: %v", err)
	}
	if result.Answer != "answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if string(result.RawResponse) != completedPayload {
		t.Fatalf("completed payload changed: got %q, want %q", result.RawResponse, completedPayload)
	}
}

func TestIsChatIntermediateAnswer(t *testing.T) {
	testCases := []struct {
		name         string
		answer       string
		intermediate bool
	}{
		{
			name:         "Chinese search status",
			answer:       "正在检索 Go 1.24 与 Go 1.25 的官方发布说明与相关文档，以便交叉核验后再比较。",
			intermediate: true,
		},
		{
			name:         "Chinese documentation lookup status",
			answer:       "正在查阅 go.dev 上 Go 1.24 与 Go 1.25 的官方发行说明，以便准确对比变化。",
			intermediate: true,
		},
		{
			name:         "English search status",
			answer:       "Let me search the official documentation before answering.",
			intermediate: true,
		},
		{
			name:         "Final concise answer",
			answer:       "Go 1.25 introduces the new experimental garbage collector, while Go 1.24 adds generic type aliases.",
			intermediate: false,
		},
		{
			name:         "Long answer containing status wording",
			answer:       strings.Repeat("This is a complete comparison with evidence. ", 20) + "I will search no further.",
			intermediate: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := isChatIntermediateAnswer(testCase.answer); actual != testCase.intermediate {
				t.Fatalf("isChatIntermediateAnswer() = %v, want %v", actual, testCase.intermediate)
			}
		})
	}
}

func TestSearchChatCompletionsContinuesIntermediateAnswer(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestCount++
		var upstreamRequest chatCompletionsRequest
		if err := json.NewDecoder(request.Body).Decode(&upstreamRequest); err != nil {
			t.Fatalf("decode request %d: %v", requestCount, err)
		}

		responseWriter.Header().Set("Content-Type", "text/event-stream")
		if requestCount == 1 {
			if len(upstreamRequest.Messages) != 1 {
				t.Fatalf("first request messages = %+v", upstreamRequest.Messages)
			}
			_, _ = responseWriter.Write([]byte(strings.Join([]string{
				`data: {"choices":[{"delta":{"content":"正在检索官方资料，以便交叉核验。"}}]}`,
				"",
				`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
				"",
				"data: [DONE]",
				"",
			}, "\n")))
			return
		}

		if len(upstreamRequest.Messages) != 3 {
			t.Fatalf("continuation request messages = %+v", upstreamRequest.Messages)
		}
		if upstreamRequest.Messages[1].Role != "assistant" || !strings.Contains(upstreamRequest.Messages[1].Content, "正在检索") {
			t.Fatalf("missing intermediate assistant message: %+v", upstreamRequest.Messages)
		}
		if upstreamRequest.Messages[2].Role != "user" || upstreamRequest.Messages[2].Content != chatFinalAnswerInstruction {
			t.Fatalf("missing final-answer instruction: %+v", upstreamRequest.Messages)
		}
		_, _ = responseWriter.Write([]byte(strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"Go 1.25 的主要升级风险包括运行时行为变化和工具链兼容性。"}}]}`,
			"",
			`data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":12,"total_tokens":32}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")))
	}))
	defer server.Close()

	client := newTestClient(t, &config.Config{
		CPABaseURL:       server.URL,
		CPAAPIKey:        "test-key",
		UpstreamProtocol: config.UpstreamProtocolChatCompletions,
		Model:            "grok-4.5",
		Timeout:          5 * time.Second,
		RegistrationMode: "free",
	})
	result, err := client.SearchStream(context.Background(), SearchRequest{
		Query:    "compare versions",
		ToolType: ToolTypeWebSearch,
	}, nil)
	if err != nil {
		t.Fatalf("SearchStream failed: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
	if strings.Contains(result.Answer, "正在检索") {
		t.Fatalf("intermediate answer escaped as final result: %q", result.Answer)
	}
	if !strings.Contains(result.Answer, "主要升级风险") {
		t.Fatalf("unexpected final answer: %q", result.Answer)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 47 {
		t.Fatalf("accumulated usage = %+v, want total 47", result.Usage)
	}
}

func TestSearchChatCompletionsRejectsPersistentIntermediateAnswer(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		requestCount++
		responseWriter.Header().Set("Content-Type", "text/event-stream")
		_, _ = responseWriter.Write([]byte(strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"正在查阅官方资料，请稍候。"}}]}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")))
	}))
	defer server.Close()

	client := newTestClient(t, &config.Config{
		CPABaseURL:       server.URL,
		CPAAPIKey:        "test-key",
		UpstreamProtocol: config.UpstreamProtocolChatCompletions,
		Model:            "grok-4.5",
		Timeout:          5 * time.Second,
		RegistrationMode: "free",
	})
	_, err := client.SearchStream(context.Background(), SearchRequest{
		Query:    "compare versions",
		ToolType: ToolTypeWebSearch,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "did not return a final answer") {
		t.Fatalf("expected persistent intermediate answer error, got %v", err)
	}
	if requestCount != maxChatContinuationAttempts+1 {
		t.Fatalf("request count = %d, want %d", requestCount, maxChatContinuationAttempts+1)
	}
}

func TestBuildAnthropicMessagesRequestBodyMapsProtocolTools(t *testing.T) {
	_, body, err := buildAnthropicMessagesRequestBody(SearchRequest{
		Query:           "latest news",
		ToolType:        ToolTypeWebSearch,
		AllowedDomains:  []string{"example.com"},
		ExcludedDomains: []string{"blocked.example"},
	}, "grok-4.3")
	if err != nil {
		t.Fatalf("build anthropic request: %v", err)
	}

	var request anthropicMessagesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode anthropic request: %v", err)
	}
	if request.MaxTokens != anthropicDefaultMaxTokens || !request.Stream {
		t.Fatalf("unexpected messages settings: %+v", request)
	}
	if len(request.Tools) != 1 || request.Tools[0].Type != "web_search_20250305" {
		t.Fatalf("unexpected web search tool: %+v", request.Tools)
	}
	if len(request.Tools[0].BlockedDomains) != 1 || request.Tools[0].BlockedDomains[0] != "blocked.example" {
		t.Fatalf("unexpected blocked domains: %+v", request.Tools[0])
	}

	_, xBody, err := buildAnthropicMessagesRequestBody(SearchRequest{
		Query:    "recent posts",
		ToolType: ToolTypeXSearch,
	}, "grok-4.3")
	if err != nil {
		t.Fatalf("build anthropic X request: %v", err)
	}
	if err := json.Unmarshal(xBody, &request); err != nil {
		t.Fatalf("decode anthropic X request: %v", err)
	}
	if request.Tools[0].Type != "web_search_20250305" || request.Tools[0].Name != "web_search" {
		t.Fatalf("unexpected X search tool: %+v", request.Tools[0])
	}
	if len(request.Tools[0].AllowedDomains) != 1 || request.Tools[0].AllowedDomains[0] != "x.com" {
		t.Fatalf("Anthropic X search must be restricted to x.com: %+v", request.Tools[0])
	}
	if len(request.Messages) != 1 || !strings.Contains(request.Messages[0].Content, anthropicXSearchInstruction) {
		t.Fatalf("Anthropic X search instruction is missing: %+v", request.Messages)
	}
	if !strings.Contains(request.Messages[0].Content, "User query: recent posts") {
		t.Fatalf("Anthropic X search query is missing: %+v", request.Messages)
	}
}

func TestParseAnthropicMessagesResponseAggregatesTextCitationsAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Answer"}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.com","title":"Example"}}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":6}}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	result, err := parseAnthropicMessagesResponse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parse anthropic response: %v", err)
	}
	if result.Answer != "Answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if len(result.Sources) != 1 || result.Sources[0].Title != "Example" {
		t.Fatalf("unexpected sources: %+v", result.Sources)
	}
	if result.Usage == nil || result.Usage.InputTokens != 4 || result.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected usage: %+v", result.Usage)
	}
}

func TestClientUsesSelectedProtocolEndpointAndHeaders(t *testing.T) {
	testCases := []struct {
		name           string
		protocol       config.UpstreamProtocol
		expectedPath   string
		expectedAPIKey string
		responseBody   string
	}{
		{
			name:         "chat completions",
			protocol:     config.UpstreamProtocolChatCompletions,
			expectedPath: "/v1/chat/completions",
			responseBody: "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name:           "anthropic messages",
			protocol:       config.UpstreamProtocolAnthropicMessages,
			expectedPath:   "/v1/messages",
			expectedAPIKey: "test-key",
			responseBody:   "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				if request.URL.Path != testCase.expectedPath {
					t.Errorf("expected path %q, got %q", testCase.expectedPath, request.URL.Path)
				}
				if request.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("missing CPA bearer authorization")
				}
				if testCase.expectedAPIKey != "" && request.Header.Get("x-api-key") != testCase.expectedAPIKey {
					t.Errorf("expected Anthropic x-api-key header")
				}
				responseWriter.Header().Set("Content-Type", "text/event-stream")
				_, _ = responseWriter.Write([]byte(testCase.responseBody))
			}))
			defer server.Close()

			client := newTestClient(t, &config.Config{
				CPABaseURL:       server.URL,
				CPAAPIKey:        "test-key",
				UpstreamProtocol: testCase.protocol,
				Model:            "grok-4.3",
				Timeout:          5 * time.Second,
				RegistrationMode: "free",
			})
			result, err := client.SearchStream(context.Background(), SearchRequest{
				Query:    "test",
				ToolType: ToolTypeWebSearch,
			}, nil)
			if err != nil {
				t.Fatalf("search selected protocol: %v", err)
			}
			if result.Answer != "ok" {
				t.Fatalf("unexpected answer: %q", result.Answer)
			}
		})
	}
}
