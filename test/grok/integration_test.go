package grok_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
)

// Live CPA integration: requires GROK_INTEGRATION_TEST=1 plus the same secrets as config.Load:
// CPA_API_KEY (upstream), GROK_JWT_SECRET (≥32 bytes, for config validation if other code paths load config).
func TestIntegrationSearchLiveCPA(t *testing.T) {
	if os.Getenv("GROK_INTEGRATION_TEST") != "1" {
		t.Skip("set GROK_INTEGRATION_TEST=1 to run live CPA integration tests")
	}
	if strings.TrimSpace(os.Getenv("CPA_API_KEY")) == "" {
		t.Skip("CPA_API_KEY is required for live CPA integration tests")
	}
	if len(strings.TrimSpace(os.Getenv("GROK_JWT_SECRET"))) < 32 {
		t.Skip("GROK_JWT_SECRET (at least 32 bytes) is required when loading full config")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load failed: %v", err)
	}

	client, err := grok.NewClientWithServerSettings(cfg.ServerSettings(), nil)
	if err != nil {
		t.Fatalf("create grok client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	var webRounds []grok.SearchRound
	webResult, err := client.SearchStream(ctx, grok.SearchRequest{
		Query:    "What is the capital of France?",
		ToolType: grok.ToolTypeWebSearch,
	}, func(round grok.SearchRound) {
		webRounds = append(webRounds, round)
		t.Logf("web search round %d: query=%q url=%q", round.Round, round.Query, round.URL)
	})
	if err != nil {
		t.Fatalf("web search stream failed: %v", err)
	}
	if strings.TrimSpace(webResult.Answer) == "" {
		t.Fatalf("web search returned empty answer")
	}
	if len(webResult.Citations) == 0 {
		t.Logf("web search returned no citations (upstream may omit them); raw: %s", prettyJSON(webResult.RawResponse))
	}
	t.Logf("web search rounds (%d): %+v", len(webRounds), webRounds)
	t.Logf("web search sources (%d): %+v", len(webResult.Sources), webResult.Sources)
	t.Logf("web search raw response (stream): %s", prettyJSON(webResult.RawResponse))

	var xRounds []grok.SearchRound
	xResult, err := client.SearchStream(ctx, grok.SearchRequest{
		Query:    "What did Elon Musk post about SpaceX recently?",
		ToolType: grok.ToolTypeXSearch,
	}, func(round grok.SearchRound) {
		xRounds = append(xRounds, round)
		t.Logf("x search round %d: query=%q url=%q", round.Round, round.Query, round.URL)
	})
	if err != nil {
		t.Fatalf("x search stream failed: %v", err)
	}
	if strings.TrimSpace(xResult.Answer) == "" {
		t.Fatalf("x search returned empty answer")
	}
	if len(xResult.Citations) == 0 {
		t.Logf("x search returned no citations (upstream may omit them); raw: %s", prettyJSON(xResult.RawResponse))
	}
	t.Logf("x search rounds (%d): %+v", len(xRounds), xRounds)
	t.Logf("x search sources (%d): %+v", len(xResult.Sources), xResult.Sources)
}

func prettyJSON(raw json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}
