package mcpserver

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/grok-mcp/internal/grok"
)

func TestServerInstructionsDocumentSearchToolUsage(t *testing.T) {
	wantedSnippets := []string{
		webSearchToolName,
		xSearchToolName,
		"query is required",
		"model is optional",
		"allowed_domains",
		"excluded_domains",
		"Do not provide allowed_domains and excluded_domains together",
		"at most 5 domains",
		"enable_image_understanding",
		"enable_image_search",
		"answer, citations, sources, and usage",
		"isError=true",
		"progressToken",
	}

	for _, wantedSnippet := range wantedSnippets {
		if !strings.Contains(ServerInstructions, wantedSnippet) {
			t.Fatalf("ServerInstructions missing %q", wantedSnippet)
		}
	}
}

func TestNewSearchToolMetadata(t *testing.T) {
	testCases := []struct {
		name        string
		title       string
		description string
	}{
		{
			name:        webSearchToolName,
			title:       webSearchToolTitle,
			description: webSearchToolDescription,
		},
		{
			name:        xSearchToolName,
			title:       xSearchToolTitle,
			description: xSearchToolDescription,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			tool := newSearchTool(testCase.name, testCase.title, testCase.description)
			if tool.Name != testCase.name {
				t.Fatalf("Name = %q, want %q", tool.Name, testCase.name)
			}
			if tool.Title != testCase.title {
				t.Fatalf("Title = %q, want %q", tool.Title, testCase.title)
			}
			if !strings.Contains(tool.Description, "query is required") {
				t.Fatalf("Description must mention query requirement; description=%q", tool.Description)
			}
			if !strings.Contains(tool.Description, "allowed_domains and excluded_domains are mutually exclusive") {
				t.Fatalf("Description must mention domain filter exclusivity; description=%q", tool.Description)
			}
			if tool.Annotations == nil {
				t.Fatalf("Annotations must be set")
			}
			if !tool.Annotations.ReadOnlyHint {
				t.Fatalf("ReadOnlyHint must be true")
			}
			if tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
				t.Fatalf("OpenWorldHint must be true")
			}
		})
	}
}

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
