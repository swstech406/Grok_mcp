package grok

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// citationCollector 对 URL 去重，同时维护扁平 citations 列表与带标题的 sources。
type citationCollector struct {
	citations []string
	sources   []Source
	seen      map[string]struct{}
}

func newCitationCollector() *citationCollector {
	return &citationCollector{seen: make(map[string]struct{})}
}

func (c *citationCollector) add(url, title string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	if _, ok := c.seen[url]; ok {
		return
	}
	c.seen[url] = struct{}{}
	c.citations = append(c.citations, url)
	c.sources = append(c.sources, Source{
		URL:   url,
		Title: strings.TrimSpace(title),
	})
}

// addRaw 兼容 citations 字段为字符串 URL 或 {url,title} 对象两种形态。
func (c *citationCollector) addRaw(raw json.RawMessage) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			c.add(s, "")
		}
		return
	}
	var ci citationItem
	if err := json.Unmarshal(raw, &ci); err == nil {
		c.add(ci.URL, ci.Title)
	}
}

// buildSearchResult 从 output 文本块、注解与顶层 citations 汇总答案与引用。
func buildSearchResult(parsed responsesResponse, rawBody []byte) (*SearchResult, error) {
	var answer strings.Builder
	collector := newCitationCollector()

	for _, item := range parsed.Output {
		for _, block := range item.Content {
			if text := strings.TrimSpace(block.Text); text != "" {
				if answer.Len() > 0 {
					answer.WriteByte('\n')
				}
				answer.WriteString(text)
			}
			for _, ann := range block.Annotations {
				if ann.Type == "url_citation" {
					collector.add(ann.URL, ann.Title)
				}
			}
		}
	}

	if len(parsed.Citations) > 0 && string(parsed.Citations) != "null" {
		var rawCites []json.RawMessage
		if err := json.Unmarshal(parsed.Citations, &rawCites); err == nil {
			for _, rc := range rawCites {
				collector.addRaw(rc)
			}
		}
	}

	usage, err := parseUsage(parsed.Usage)
	if err != nil {
		return nil, err
	}

	answerText := strings.TrimSpace(answer.String())
	if answerText == "" {
		return nil, fmt.Errorf("upstream response did not contain answer text")
	}

	return &SearchResult{
		Answer:      answerText,
		Citations:   collector.citations,
		Sources:     collector.sources,
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