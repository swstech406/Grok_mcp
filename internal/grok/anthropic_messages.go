package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const anthropicDefaultMaxTokens = 4096

const anthropicXSearchInstruction = "Search public posts on X (x.com) only. " +
	"Use the web search tool to find relevant posts, then return a final answer with direct X post URLs."

type anthropicMessagesRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicTool struct {
	Type           string         `json:"type,omitempty"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	MaxUses        int            `json:"max_uses,omitempty"`
	AllowedDomains []string       `json:"allowed_domains,omitempty"`
	BlockedDomains []string       `json:"blocked_domains,omitempty"`
	InputSchema    map[string]any `json:"input_schema,omitempty"`
}

type anthropicMessagesResponse struct {
	Type    string                  `json:"type"`
	Content []anthropicContentBlock `json:"content"`
	Usage   anthropicUsage          `json:"usage"`
	Message *anthropicMessageResult `json:"message"`
	Delta   anthropicDelta          `json:"delta"`
}

type anthropicMessageResult struct {
	Content []anthropicContentBlock `json:"content"`
	Usage   anthropicUsage          `json:"usage"`
}

type anthropicContentBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text"`
	Citations []anthropicCitation   `json:"citations"`
	Content   []anthropicTextResult `json:"content"`
}

type anthropicTextResult struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Text  string `json:"text"`
}

type anthropicDelta struct {
	Type     string            `json:"type"`
	Text     string            `json:"text"`
	Citation anthropicCitation `json:"citation"`
	Usage    anthropicUsage    `json:"usage"`
}

type anthropicCitation struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (s clientSnapshot) searchAnthropicMessages(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	model, body, err := buildAnthropicMessagesRequestBody(req, s.defaultModel)
	if err != nil {
		return nil, err
	}
	s.log.Debugf("SearchStream start protocol=%s model=%s tool=%s query=%q", s.protocol, model, req.ToolType, req.Query)

	response, err := s.postJSON(ctx, "/v1/messages", body, true)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, s.httpError(response)
	}
	return parseAnthropicMessagesResponse(response.Body)
}

func buildAnthropicMessagesRequestBody(req SearchRequest, defaultModel string) (string, []byte, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = defaultModel
	}
	if err := validateModel(model); err != nil {
		return "", nil, err
	}

	messageContent := req.Query
	tool := anthropicTool{
		Type:           "web_search_20250305",
		Name:           "web_search",
		MaxUses:        8,
		AllowedDomains: []string{"x.com"},
	}
	if req.ToolType == ToolTypeWebSearch {
		tool = anthropicTool{
			Type:           "web_search_20250305",
			Name:           "web_search",
			MaxUses:        8,
			AllowedDomains: req.AllowedDomains,
			BlockedDomains: req.ExcludedDomains,
		}
	} else {
		messageContent = anthropicXSearchInstruction + "\n\nUser query: " + req.Query
	}

	upstreamRequest := anthropicMessagesRequest{
		Model:     model,
		MaxTokens: anthropicDefaultMaxTokens,
		Messages:  []anthropicMessage{{Role: "user", Content: messageContent}},
		Tools:     []anthropicTool{tool},
		Stream:    true,
	}
	body, err := json.Marshal(upstreamRequest)
	if err != nil {
		return "", nil, fmt.Errorf("marshal anthropic messages request: %w", err)
	}
	return model, body, nil
}

func parseAnthropicMessagesResponse(body io.Reader) (*SearchResult, error) {
	rawBody, err := readAllUpstreamResponse(body)
	if err != nil {
		return nil, fmt.Errorf("read anthropic messages response: %w", err)
	}

	var answer strings.Builder
	collector := newCitationCollector()
	usage := anthropicUsage{}
	consumeResponse := func(response anthropicMessagesResponse) error {
		if response.Type == "error" {
			return fmt.Errorf("upstream stream error: %s", string(rawBody))
		}
		if response.Message != nil {
			if err := collectAnthropicContent(&answer, collector, response.Message.Content); err != nil {
				return err
			}
			mergeAnthropicUsage(&usage, response.Message.Usage)
		}
		if err := collectAnthropicContent(&answer, collector, response.Content); err != nil {
			return err
		}
		mergeAnthropicUsage(&usage, response.Usage)
		if response.Delta.Type == "text_delta" {
			if err := appendAnswerText(&answer, response.Delta.Text); err != nil {
				return err
			}
		}
		if response.Delta.Type == "citations_delta" {
			collector.add(response.Delta.Citation.URL, response.Delta.Citation.Title)
		}
		mergeAnthropicUsage(&usage, response.Delta.Usage)
		return collector.err
	}

	isSSE := bytes.Contains(rawBody, []byte("data:"))
	if isSSE {
		sawMessageStop := false
		err = forEachSSEEvent(bytes.NewReader(rawBody), func(payload []byte) error {
			var response anthropicMessagesResponse
			if decodeErr := json.Unmarshal(payload, &response); decodeErr != nil {
				return fmt.Errorf("decode anthropic stream event: %w", decodeErr)
			}
			if response.Type == "message_stop" {
				sawMessageStop = true
			}
			return consumeResponse(response)
		})
		if err == nil && !sawMessageStop {
			return nil, fmt.Errorf("upstream anthropic messages stream ended prematurely without message_stop event")
		}
	} else {
		var response anthropicMessagesResponse
		err = json.Unmarshal(rawBody, &response)
		if err == nil {
			err = consumeResponse(response)
		}
	}
	if err != nil {
		return nil, err
	}

	answerText := strings.TrimSpace(answer.String())
	if answerText == "" {
		return nil, fmt.Errorf("upstream response did not contain answer text")
	}
	var normalizedUsage *Usage
	if usage.InputTokens != 0 || usage.OutputTokens != 0 {
		normalizedUsage = &Usage{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			TotalTokens:  usage.InputTokens + usage.OutputTokens,
		}
	}
	return &SearchResult{
		Answer:      answerText,
		Citations:   collector.citations,
		Sources:     collector.sources,
		Usage:       normalizedUsage,
		RawResponse: json.RawMessage(rawBody),
	}, nil
}

func collectAnthropicContent(answer *strings.Builder, collector *citationCollector, blocks []anthropicContentBlock) error {
	for _, block := range blocks {
		if err := appendAnswerText(answer, block.Text); err != nil {
			return err
		}
		for _, citation := range block.Citations {
			collector.add(citation.URL, citation.Title)
		}
		for _, result := range block.Content {
			collector.add(result.URL, result.Title)
		}
		if collector.err != nil {
			return collector.err
		}
	}
	return nil
}

func mergeAnthropicUsage(current *anthropicUsage, update anthropicUsage) {
	if update.InputTokens != 0 {
		current.InputTokens = update.InputTokens
	}
	if update.OutputTokens != 0 {
		current.OutputTokens = update.OutputTokens
	}
}
