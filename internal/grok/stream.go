package grok

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/logx"
)

type searchRoundTracker struct {
	nextRound int
	seen      map[string]struct{}
}

func newSearchRoundTracker() *searchRoundTracker {
	return &searchRoundTracker{seen: make(map[string]struct{})}
}

// emitSearchRound recognizes completed Responses search output items. Other
// event shapes are ignored so they cannot create false progress notifications.
func (t *searchRoundTracker) emitSearchRound(eventType string, item streamOutputItem, onRound func(SearchRound), log *logx.Logger) error {
	if eventType != "response.output_item.done" || !isSearchCallItem(item.Type) {
		return nil
	}
	itemType := item.Type

	query := firstNonEmptyString(
		item.Action.Query,
		item.Query,
	)
	url := firstNonEmptyString(
		item.Action.URL,
		item.URL,
	)
	eventID := item.ID
	deduplicationKey := eventID
	if deduplicationKey == "" {
		deduplicationKey = itemType + "\x00" + query + "\x00" + url
	}
	if _, alreadyEmitted := t.seen[deduplicationKey]; alreadyEmitted {
		return nil
	}
	if t.nextRound >= maxSearchRoundCount {
		return fmt.Errorf("upstream stream exceeded search round limit of %d", maxSearchRoundCount)
	}
	t.seen[deduplicationKey] = struct{}{}

	t.nextRound++
	searchRound := SearchRound{Round: t.nextRound, Query: query, URL: url}
	logStreamRound(log, itemType, searchRound)
	if onRound != nil {
		onRound(searchRound)
	}
	return nil
}

// parseSearchStream 消费上游 SSE，在 web_search_call 或 x_search_call 完成时回调 onRound，
// 并在收到 response.completed 后从该事件的 response 字段构建 SearchResult。
func parseSearchStream(body io.Reader, onRound func(SearchRound), log *logx.Logger) (*SearchResult, error) {
	var completedBody []byte
	searchRounds := newSearchRoundTracker()

	err := forEachSSEEvent(body, func(payload []byte) error {
		var event streamEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}

		switch event.Type {
		case "response.output_item.done":
			// action（query/url）只在 output_item.done 时才完整。
			// CPA 源码证据：output_item.added 的 item 只有 {id,type,status}，无 action。
			if err := searchRounds.emitSearchRound(event.Type, event.Item, onRound, log); err != nil {
				return err
			}
		case "response.completed":
			completedBody = bytes.Clone(payload)
		case "error":
			return newUpstreamStreamError("responses", event.Type, len(payload))
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

func isSearchCallItem(itemType string) bool {
	return itemType == "web_search_call" || itemType == "x_search_call"
}

func logStreamRound(log *logx.Logger, itemType string, searchRound SearchRound) {
	if log == nil {
		return
	}
	log.Debugf(
		"search round event=%s round=%d query_bytes=%d has_source_url=%t",
		itemType,
		searchRound.Round,
		len(searchRound.Query),
		searchRound.URL != "",
	)
}
