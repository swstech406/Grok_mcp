package grok

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SearchStream performs a streaming search and invokes onRound for each upstream
// web_search_call action before returning the final parsed result.
func (c *Client) SearchStream(ctx context.Context, req SearchRequest, onRound func(SearchRound)) (*SearchResult, error) {
	if err := validateSearchRequest(req); err != nil {
		return nil, err
	}

	model, body, err := c.buildSearchRequestBody(req, true)
	if err != nil {
		return nil, err
	}
	c.debugf("SearchStream start model=%s tool=%s query=%q", model, req.ToolType, truncate(req.Query, 80))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			c.debugf("upstream HTTP %d read body error: %v", resp.StatusCode, readErr)
			return nil, fmt.Errorf("upstream returned HTTP %d: read body: %w", resp.StatusCode, readErr)
		}
		c.debugf("upstream HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 256))
		return nil, fmt.Errorf("upstream returned HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 1024))
	}

	result, err := parseSearchStream(resp.Body, onRound, c.debugf)
	if err != nil {
		return nil, err
	}
	if result.Usage != nil {
		c.debugf("SearchStream done tokens=%d", result.Usage.TotalTokens)
	}
	return result, nil
}

func parseSearchStream(body io.Reader, onRound func(SearchRound), debugf func(string, ...any)) (*SearchResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var dataLines []string
	round := 0
	var completedBody []byte

	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = nil

		if payload == "[DONE]" {
			return nil
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}

		switch event.Type {
		case "response.output_item.done":
			// action（query/url）只在 output_item.done 时才完整。
			// CPA 源码证据：output_item.added 的 item 只有 {id,type,status}，无 action。
			if event.Item.Type == "web_search_call" {
				round++
				sr := SearchRound{
					Round: round,
					Query: strings.TrimSpace(event.Item.Action.Query),
					URL:   strings.TrimSpace(event.Item.Action.URL),
				}
				if debugf != nil {
					if sr.Query != "" {
						debugf("web_search_call round=%d query=%q", sr.Round, sr.Query)
					} else if sr.URL != "" {
						debugf("web_search_call round=%d url=%s", sr.Round, sr.URL)
					} else {
						debugf("web_search_call round=%d", sr.Round)
					}
				}
				if onRound != nil {
					onRound(sr)
				}
			}
		case "response.completed":
			completedBody = []byte(payload)
		case "error":
			return fmt.Errorf("upstream stream error: %s", truncate(payload, 1024))
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flushEvent(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	if err := flushEvent(); err != nil {
		return nil, err
	}

	if len(completedBody) == 0 {
		return nil, fmt.Errorf("upstream stream ended without response.completed event")
	}

	var completed streamEvent
	if err := json.Unmarshal(completedBody, &completed); err != nil {
		return nil, fmt.Errorf("decode response.completed: %w", err)
	}

	result, err := buildSearchResult(completed.Response, completedBody)
	if err != nil {
		return nil, err
	}
	return result, nil
}
