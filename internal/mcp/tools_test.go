package mcpserver

import (
	"testing"

	"github.com/grok-mcp/internal/grok"
	"github.com/google/jsonschema-go/jsonschema"
)

// TestSearchInputSchema 锁住 L1 修复：query 的 required 由 json tag（无 omitempty）自动推断，
// jsonschema tag 仅作 description，不能带 "required," 前缀污染描述文本。
func TestSearchInputSchema(t *testing.T) {
	schema, err := jsonschema.For[SearchInput](nil)
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}

	required := false
	for _, r := range schema.Required {
		if r == "query" {
			required = true
		}
	}
	if !required {
		t.Fatalf("query must be required; required=%v", schema.Required)
	}

	prop := schema.Properties["query"]
	if prop == nil {
		t.Fatalf("query property missing from schema")
	}
	if prop.Description != "Search query text" {
		t.Fatalf("query description = %q, want %q", prop.Description, "Search query text")
	}
}

func TestFormatSearchRoundMessageSearch(t *testing.T) {
	got := formatSearchRoundMessage(grok.SearchRound{Round: 1, Query: "capital of France"})
	want := `🔍 第1轮：搜索 "capital of France"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatSearchRoundMessageFetch(t *testing.T) {
	got := formatSearchRoundMessage(grok.SearchRound{Round: 2, URL: "https://example.com/france"})
	want := "📄 第2轮：读取 https://example.com/france"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatSearchRoundMessageEmpty(t *testing.T) {
	got := formatSearchRoundMessage(grok.SearchRound{Round: 3})
	want := "🔍 第3轮：搜索中"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}