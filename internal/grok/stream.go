package grok

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/grok-mcp/internal/logx"
)

// parseSearchStream 消费上游 SSE，在 web_search_call 完成时回调 onRound，
// 并在收到 response.completed 后从该事件的 response 字段构建 SearchResult。
func parseSearchStream(body io.Reader, onRound func(SearchRound), log *logx.Logger) (*SearchResult, error) {
	round := 0
	var completedBody []byte

	err := forEachSSEEvent(body, func(payload string) error {
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
				logStreamRound(log, sr)
				if onRound != nil {
					onRound(sr)
				}
			}
		case "response.completed":
			completedBody = []byte(payload)
		case "error":
			return fmt.Errorf("upstream stream error: %s", logx.Truncate(payload, 1024))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(completedBody) == 0 {
		return nil, fmt.Errorf("upstream stream ended without response.completed event")
	}

	var completed streamEvent
	if err := json.Unmarshal(completedBody, &completed); err != nil {
		return nil, fmt.Errorf("decode response.completed: %w", err)
	}

	return buildSearchResult(completed.Response, completedBody)
}

func logStreamRound(log *logx.Logger, sr SearchRound) {
	if sr.Query != "" {
		log.Debugf("web_search_call round=%d query=%q", sr.Round, sr.Query)
	} else if sr.URL != "" {
		log.Debugf("web_search_call round=%d url=%s", sr.Round, sr.URL)
	} else {
		log.Debugf("web_search_call round=%d", sr.Round)
	}
}