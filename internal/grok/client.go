package grok

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/grok-mcp/internal/config"
)

type Client struct {
	baseURL      string
	apiKey       string
	defaultModel string
	httpClient   *http.Client
	debug        bool
}

func NewClient(cfg *config.Config) *Client {
	log.SetOutput(os.Stderr)
	return &Client{
		baseURL:      cfg.CPABaseURL,
		apiKey:       cfg.CPAAPIKey,
		defaultModel: cfg.Model,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		debug: cfg.Debug,
	}
}

func (c *Client) debugf(format string, args ...any) {
	if !c.debug {
		return
	}
	log.Printf("[grok] "+format, args...)
}

func (c *Client) buildToolDef(req SearchRequest) toolDef {
	tool := toolDef{Type: string(req.ToolType)}

	if req.ToolType == ToolTypeWebSearch {
		tool.AllowedDomains = req.AllowedDomains
		tool.ExcludedDomains = req.ExcludedDomains
		tool.EnableImageUnderstanding = req.EnableImageUnderstanding
		tool.EnableImageSearch = req.EnableImageSearch
	}

	return tool
}

func validateSearchRequest(req SearchRequest) error {
	if strings.TrimSpace(req.Query) == "" {
		return fmt.Errorf("query must not be empty")
	}
	if req.ToolType != ToolTypeWebSearch && req.ToolType != ToolTypeXSearch {
		return fmt.Errorf("unsupported tool type: %q", req.ToolType)
	}
	if len(req.AllowedDomains) > 0 && len(req.ExcludedDomains) > 0 {
		return fmt.Errorf("allowed_domains and excluded_domains cannot be used together")
	}
	if len(req.AllowedDomains) > 5 {
		return fmt.Errorf("allowed_domains supports at most 5 entries")
	}
	if len(req.ExcludedDomains) > 5 {
		return fmt.Errorf("excluded_domains supports at most 5 entries")
	}
	return nil
}

func (c *Client) buildSearchRequestBody(req SearchRequest, stream bool) (string, []byte, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.defaultModel
	}

	upstreamReq := responsesRequest{
		Model:  model,
		Input:  []inputMessage{{Role: "user", Content: req.Query}},
		Tools:  []toolDef{c.buildToolDef(req)},
		Stream: stream,
	}

	body, err := json.Marshal(upstreamReq)
	if err != nil {
		return "", nil, fmt.Errorf("marshal request: %w", err)
	}
	return model, body, nil
}

func buildSearchResult(parsed responsesResponse, rawBody []byte) (*SearchResult, error) {
	answerParts := make([]string, 0)
	citationSet := make(map[string]struct{})
	citations := make([]string, 0)
	sourceSet := make(map[string]struct{})
	sources := make([]Source, 0)

	for _, item := range parsed.Output {
		for _, block := range item.Content {
			if text := strings.TrimSpace(block.Text); text != "" {
				answerParts = append(answerParts, text)
			}
			for _, ann := range block.Annotations {
				if ann.Type == "url_citation" {
					addCitation(citationSet, &citations, ann.URL)
					addSource(sourceSet, &sources, ann.URL, ann.Title)
				}
			}
		}
	}

	if len(parsed.Citations) > 0 && string(parsed.Citations) != "null" {
		var rawCites []json.RawMessage
		if err := json.Unmarshal(parsed.Citations, &rawCites); err == nil {
			for _, rc := range rawCites {
				extractCitation(rc, citationSet, &citations, sourceSet, &sources)
			}
		}
	}

	usage, err := parseUsage(parsed.Usage)
	if err != nil {
		return nil, err
	}

	answer := strings.TrimSpace(strings.Join(answerParts, "\n"))
	if answer == "" {
		return nil, fmt.Errorf("upstream response did not contain answer text")
	}

	return &SearchResult{
		Answer:      answer,
		Citations:   citations,
		Sources:     sources,
		Usage:       usage,
		RawResponse: json.RawMessage(rawBody),
	}, nil
}

func parseUsage(raw json.RawMessage) (*Usage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var usage Usage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, fmt.Errorf("decode usage: %w", err)
	}
	return &usage, nil
}

func addCitation(seen map[string]struct{}, citations *[]string, url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	if _, ok := seen[url]; ok {
		return
	}
	seen[url] = struct{}{}
	*citations = append(*citations, url)
}

func addSource(seen map[string]struct{}, sources *[]Source, url, title string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	if _, ok := seen[url]; ok {
		return
	}
	seen[url] = struct{}{}
	*sources = append(*sources, Source{
		URL:   url,
		Title: strings.TrimSpace(title),
	})
}

func extractCitation(raw []byte, seen map[string]struct{}, citations *[]string, sourceSeen map[string]struct{}, sources *[]Source) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			addCitation(seen, citations, s)
			addSource(sourceSeen, sources, s, "")
		}
		return
	}
	var ci citationItem
	if err := json.Unmarshal(raw, &ci); err == nil {
		addCitation(seen, citations, ci.URL)
		addSource(sourceSeen, sources, ci.URL, ci.Title)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
