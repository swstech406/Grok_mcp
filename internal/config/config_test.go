package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

// panelEnv 提供 Load 所需的最小环境变量，包括满足最小长度校验的 JWT 密钥。
func panelEnv(t *testing.T) {
	t.Helper()
	setEnv(t, "CPA_API_KEY", "test-key")
	setEnv(t, "GROK_JWT_SECRET", "jwt-secret-must-be-at-least-32-bytes!")
}

func TestLoadRequiresAPIKey(t *testing.T) {
	setEnv(t, "CPA_API_KEY", "")
	setEnv(t, "GROK_JWT_SECRET", "jwt-secret")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CPA_API_KEY is required") {
		t.Fatalf("expected CPA_API_KEY error, got %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	panelEnv(t)
	setEnv(t, "CPA_BASE_URL", "")
	setEnv(t, "GROK_MODEL", "")
	setEnv(t, "GROK_HTTP_TIMEOUT", "")
	setEnv(t, "GROK_MCP_DEBUG", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CPABaseURL != "http://127.0.0.1:8317" {
		t.Fatalf("unexpected base URL: %s", cfg.CPABaseURL)
	}
	if cfg.Model != "grok-4.3" {
		t.Fatalf("unexpected model: %s", cfg.Model)
	}
	if cfg.Timeout != 120*time.Second {
		t.Fatalf("unexpected timeout: %v", cfg.Timeout)
	}
	if cfg.Debug {
		t.Fatalf("expected debug disabled by default")
	}
}

func TestLoadCustomTimeout(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_HTTP_TIMEOUT", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Timeout != 45*time.Second {
		t.Fatalf("unexpected timeout: %v", cfg.Timeout)
	}
}

func TestLoadInvalidTimeout(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_HTTP_TIMEOUT", "abc")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "GROK_HTTP_TIMEOUT must be a positive integer") {
		t.Fatalf("expected timeout validation error, got %v", err)
	}
}

func TestLoadDebugParsing(t *testing.T) {
	panelEnv(t)

	for _, value := range []string{"1", "true", "yes"} {
		setEnv(t, "GROK_MCP_DEBUG", value)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load failed for %q: %v", value, err)
		}
		if !cfg.Debug {
			t.Fatalf("expected debug enabled for %q", value)
		}
	}

	setEnv(t, "GROK_MCP_DEBUG", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Debug {
		t.Fatalf("expected debug disabled for 0")
	}
}

func TestLoadRequiresJWTSecret(t *testing.T) {
	setEnv(t, "CPA_API_KEY", "test-key")
	setEnv(t, "GROK_JWT_SECRET", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "GROK_JWT_SECRET is required") {
		t.Fatalf("expected jwt secret error, got %v", err)
	}
}

func TestLoadRejectsShortJWTSecret(t *testing.T) {
	setEnv(t, "CPA_API_KEY", "test-key")
	setEnv(t, "GROK_JWT_SECRET", "a")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("expected short jwt secret error, got %v", err)
	}
}

func TestLoadHTTPDefaults(t *testing.T) {
	panelEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DBPath != "./grok-mcp.db" || cfg.DefaultUserRPM != 60 {
		t.Fatalf("unexpected http defaults: %+v", cfg)
	}
}

func TestParseBoolEnvUnset(t *testing.T) {
	_ = os.Unsetenv("GROK_MCP_DEBUG")
	if parseBoolEnv("GROK_MCP_DEBUG") {
		t.Fatalf("expected false for unset env")
	}
}