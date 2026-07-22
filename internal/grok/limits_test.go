package grok

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestUpstreamProtocolsRejectOversizedResponses(t *testing.T) {
	oversizedBody := strings.Repeat(":\n", int(maxUpstreamResponseBytes/2)+1)
	testCases := []struct {
		name  string
		parse func() error
	}{
		{
			name: "responses",
			parse: func() error {
				_, err := parseSearchStream(strings.NewReader(oversizedBody), nil, nil)
				return err
			},
		},
		{
			name: "chat completions",
			parse: func() error {
				_, err := parseChatCompletionsResponse(strings.NewReader(oversizedBody), nil, nil)
				return err
			},
		},
		{
			name: "anthropic messages",
			parse: func() error {
				_, err := parseAnthropicMessagesResponse(strings.NewReader(oversizedBody))
				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.parse()
			if err == nil {
				t.Fatal("expected total byte limit error")
			}
			if testCase.name == "anthropic messages" {
				var upstreamError *UpstreamError
				if !errors.As(err, &upstreamError) || upstreamError.Category != UpstreamErrorCategoryResponseRead {
					t.Fatalf("expected safe upstream response-read error, got %v", err)
				}
				return
			}
			if !strings.Contains(err.Error(), "total byte limit") {
				t.Fatalf("expected total byte limit error, got %v", err)
			}
		})
	}
}

func TestForEachSSEEventAcceptsEventAtByteLimit(t *testing.T) {
	payload := strings.Repeat("a", maxSSEEventBytes)
	stream := "data: " + payload + "\n\n"

	processedEvents := 0
	err := forEachSSEEvent(strings.NewReader(stream), func(receivedPayload []byte) error {
		processedEvents++
		if len(receivedPayload) != maxSSEEventBytes {
			t.Fatalf("payload length = %d, want %d", len(receivedPayload), maxSSEEventBytes)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("parse event at byte limit: %v", err)
	}
	if processedEvents != 1 {
		t.Fatalf("processed event count = %d, want 1", processedEvents)
	}
}

func TestForEachSSEEventRejectsEventOneByteOverLimit(t *testing.T) {
	payload := strings.Repeat("a", maxSSEEventBytes+1)
	stream := "data: " + payload + "\n\n"

	err := forEachSSEEvent(strings.NewReader(stream), func([]byte) error {
		t.Fatal("oversized event must not reach callback")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "stream event exceeded byte limit") {
		t.Fatalf("expected explicit event byte limit error, got %v", err)
	}
}

func TestForEachSSEEventCountsMultilineSeparatorBytes(t *testing.T) {
	firstDataLine := strings.Repeat("a", maxSSEEventBytes-1)
	stream := "data: " + firstDataLine + "\ndata: b\n\n"

	err := forEachSSEEvent(strings.NewReader(stream), func([]byte) error {
		t.Fatal("oversized multiline event must not reach callback")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "stream event exceeded byte limit") {
		t.Fatalf("expected multiline event byte limit error, got %v", err)
	}
}

func TestForEachSSEEventRejectsAggregateStreamOverTotalLimit(t *testing.T) {
	event := "data: " + strings.Repeat("a", 2048) + "\n\n"
	eventCount := int(maxUpstreamResponseBytes)/len(event) + 1
	stream := strings.Repeat(event, eventCount)

	err := forEachSSEEvent(strings.NewReader(stream), func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "total byte limit") {
		t.Fatalf("expected total byte limit error, got %v", err)
	}
}

func TestUpstreamSSEProtocolsRejectOversizedEvents(t *testing.T) {
	oversizedStream := "data: " + strings.Repeat("a", maxSSEEventBytes+1) + "\n\n"
	testCases := []struct {
		name  string
		parse func() error
	}{
		{
			name: "responses",
			parse: func() error {
				_, err := parseSearchStream(strings.NewReader(oversizedStream), nil, nil)
				return err
			},
		},
		{
			name: "chat completions",
			parse: func() error {
				_, err := parseChatCompletionsResponse(strings.NewReader(oversizedStream), nil, nil)
				return err
			},
		},
		{
			name: "anthropic messages",
			parse: func() error {
				_, err := parseAnthropicMessagesResponse(strings.NewReader(oversizedStream))
				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.parse()
			if err == nil || !strings.Contains(err.Error(), "stream event exceeded byte limit") {
				t.Fatalf("expected event byte limit error, got %v", err)
			}
		})
	}
}

func TestForEachSSEEventIgnoresDoneAndNonDataFields(t *testing.T) {
	stream := "event: message\nid: 1\ndata: [DONE]\n\n: comment\ndata: {}\n\n"
	processedEvents := 0
	err := forEachSSEEvent(strings.NewReader(stream), func(payload []byte) error {
		processedEvents++
		if string(payload) != "{}" {
			t.Fatalf("unexpected payload: %q", payload)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("parse SSE control fields: %v", err)
	}
	if processedEvents != 1 {
		t.Fatalf("processed event count = %d, want 1", processedEvents)
	}
}

func TestForEachSSEEventRejectsExcessiveEventCount(t *testing.T) {
	var stream strings.Builder
	for eventIndex := 0; eventIndex <= maxSSEEventCount; eventIndex++ {
		stream.WriteString("data: {}\n\n")
	}

	processedEvents := 0
	err := forEachSSEEvent(strings.NewReader(stream.String()), func([]byte) error {
		processedEvents++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "event limit") {
		t.Fatalf("expected event limit error, got %v", err)
	}
	if processedEvents != maxSSEEventCount {
		t.Fatalf("processed %d events before rejection, want %d", processedEvents, maxSSEEventCount)
	}
}

func TestParseSearchStreamRejectsExcessiveSearchRounds(t *testing.T) {
	var stream strings.Builder
	for roundIndex := 0; roundIndex <= maxSearchRoundCount; roundIndex++ {
		payload := fmt.Sprintf(
			`{"type":"response.output_item.done","item":{"id":"search_%d","type":"web_search_call","action":{"query":"query %d"}}}`,
			roundIndex,
			roundIndex,
		)
		stream.WriteString("data: ")
		stream.WriteString(payload)
		stream.WriteString("\n\n")
	}

	processedRounds := 0
	_, err := parseSearchStream(strings.NewReader(stream.String()), func(SearchRound) {
		processedRounds++
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "search round limit") {
		t.Fatalf("expected search round limit error, got %v", err)
	}
	if processedRounds != maxSearchRoundCount {
		t.Fatalf("processed %d rounds before rejection, want %d", processedRounds, maxSearchRoundCount)
	}
}

func TestUpstreamProtocolsRejectOversizedAggregatedAnswers(t *testing.T) {
	oversizedAnswer := strings.Repeat("a", maxAggregatedAnswerBytes+1)
	testCases := []struct {
		name  string
		parse func() error
	}{
		{
			name: "responses",
			parse: func() error {
				_, err := buildSearchResult(responsesResponse{
					Output: []outputItem{{
						Content: []contentBlock{{Text: oversizedAnswer}},
					}},
				}, nil)
				return err
			},
		},
		{
			name: "chat completions",
			parse: func() error {
				body, marshalErr := json.Marshal(chatCompletionsResponse{
					Choices: []chatChoice{{
						Message: chatResponseMessage{Content: oversizedAnswer},
					}},
				})
				if marshalErr != nil {
					return marshalErr
				}
				_, err := parseChatCompletionsResponse(strings.NewReader(string(body)), nil, nil)
				return err
			},
		},
		{
			name: "anthropic messages",
			parse: func() error {
				body, marshalErr := json.Marshal(anthropicMessagesResponse{
					Content: []anthropicContentBlock{{Text: oversizedAnswer}},
				})
				if marshalErr != nil {
					return marshalErr
				}
				_, err := parseAnthropicMessagesResponse(strings.NewReader(string(body)))
				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.parse()
			if err == nil || !strings.Contains(err.Error(), "aggregated answer byte limit") {
				t.Fatalf("expected aggregated answer limit error, got %v", err)
			}
		})
	}
}

func TestUpstreamProtocolsRejectExcessiveCitations(t *testing.T) {
	rawCitations := make([]json.RawMessage, 0, maxCitationCount+1)
	responseAnnotations := make([]annotation, 0, maxCitationCount+1)
	anthropicCitations := make([]anthropicCitation, 0, maxCitationCount+1)
	for citationIndex := 0; citationIndex <= maxCitationCount; citationIndex++ {
		citationURL := fmt.Sprintf("https://example.com/source/%d", citationIndex)
		rawCitations = append(rawCitations, json.RawMessage(fmt.Sprintf(`{"url":%q}`, citationURL)))
		responseAnnotations = append(responseAnnotations, annotation{Type: "url_citation", URL: citationURL})
		anthropicCitations = append(anthropicCitations, anthropicCitation{URL: citationURL})
	}
	topLevelCitations, err := json.Marshal(rawCitations)
	if err != nil {
		t.Fatalf("marshal response citations: %v", err)
	}

	testCases := []struct {
		name  string
		parse func() error
	}{
		{
			name: "responses",
			parse: func() error {
				_, err := buildSearchResult(responsesResponse{
					Output: []outputItem{{Content: []contentBlock{{
						Text:        "answer",
						Annotations: responseAnnotations,
					}}}},
					Citations: topLevelCitations,
				}, nil)
				return err
			},
		},
		{
			name: "chat completions",
			parse: func() error {
				body, marshalErr := json.Marshal(chatCompletionsResponse{
					Choices:   []chatChoice{{Message: chatResponseMessage{Content: "answer"}}},
					Citations: rawCitations,
				})
				if marshalErr != nil {
					return marshalErr
				}
				_, err := parseChatCompletionsResponse(strings.NewReader(string(body)), nil, nil)
				return err
			},
		},
		{
			name: "anthropic messages",
			parse: func() error {
				body, marshalErr := json.Marshal(anthropicMessagesResponse{
					Content: []anthropicContentBlock{{
						Text:      "answer",
						Citations: anthropicCitations,
					}},
				})
				if marshalErr != nil {
					return marshalErr
				}
				_, err := parseAnthropicMessagesResponse(strings.NewReader(string(body)))
				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.parse()
			if err == nil || !strings.Contains(err.Error(), "citation limit") {
				t.Fatalf("expected citation limit error, got %v", err)
			}
		})
	}
}

func TestCitationCollectorRejectsOversizedCitationData(t *testing.T) {
	collector := newCitationCollector()
	collector.add("https://example.com/"+strings.Repeat("a", maxAggregatedCitationBytes), "")
	if collector.err == nil || !strings.Contains(collector.err.Error(), "aggregated citation byte limit") {
		t.Fatalf("expected aggregated citation byte limit error, got %v", collector.err)
	}
}
