package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeJSONExcludesCPAAPIKey(t *testing.T) {
	const sensitiveAPIKey = "cpa-test-never-return-this-full-secret-7f0d5b"
	runtimeSettings := Runtime{
		CPABaseURL: "https://cpa.example.test",
		CPAAPIKey:  sensitiveAPIKey,
		Model:      "grok-4.3",
	}

	encodedSettings, err := json.Marshal(runtimeSettings)
	if err != nil {
		t.Fatalf("marshal runtime settings: %v", err)
	}
	encodedText := string(encodedSettings)
	if strings.Contains(encodedText, sensitiveAPIKey) {
		t.Fatalf("runtime settings JSON exposed CPA API key: %s", encodedText)
	}
	if strings.Contains(encodedText, "CPAAPIKey") {
		t.Fatalf("runtime settings JSON included CPA API key field: %s", encodedText)
	}
	if !strings.Contains(encodedText, "https://cpa.example.test") {
		t.Fatalf("runtime settings JSON omitted non-secret fields: %s", encodedText)
	}
}
