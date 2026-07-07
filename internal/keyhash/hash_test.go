package keyhash_test

import (
	"strings"
	"testing"

	"github.com/grok-mcp/internal/keyhash"
)

func TestHashAPIKey_golden(t *testing.T) {
	raw := "grok_testtoken"
	got := keyhash.HashAPIKey(raw)
	want := "e13c4b7961b77d7e32720c2b2d69590e0dd0c2bc12382936a6ef6dfdd822b75e"
	if got != want {
		t.Fatalf("HashAPIKey(%q) = %q, want %q", raw, got, want)
	}
}

func TestHashAPIKey_empty(t *testing.T) {
	got := keyhash.HashAPIKey("")
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Fatalf("HashAPIKey(\"\") = %q, want %q", got, want)
	}
}

func TestHashAPIKey_longInput(t *testing.T) {
	raw := strings.Repeat("a", 10_000)
	got := keyhash.HashAPIKey(raw)
	if len(got) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(got))
	}
	if got != keyhash.HashAPIKey(raw) {
		t.Fatal("expected deterministic hash for long input")
	}
}
