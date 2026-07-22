package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	oldValue, hadValue := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("failed to unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(key, oldValue)
			return
		}
		_ = os.Unsetenv(key)
	})
}

// panelEnv 提供 Load 所需的最小环境变量，包括满足最小长度校验的 JWT 密钥。
func panelEnv(t *testing.T) {
	t.Helper()
	unsetEnv(t, "GROK_PROXY_URL")
	unsetEnv(t, "GROK_PROXY_ENABLED")
	unsetEnv(t, "GROK_USAGE_RAW_RETENTION_DAYS")
	unsetEnv(t, "GROK_USAGE_HOURLY_RETENTION_DAYS")
	unsetEnv(t, "GROK_USAGE_DAILY_RETENTION_DAYS")
	unsetEnv(t, "GROK_USAGE_MAINTENANCE_INTERVAL")
	unsetEnv(t, "GROK_CLIENT_IP_MODE")
	unsetEnv(t, "GROK_TRUSTED_PROXY_CIDRS")
	unsetEnv(t, "GROK_INITIAL_REGISTRATION_MODE")
	unsetEnv(t, "GROK_AUTH_PASSWORD_MAX_CONCURRENT")
	unsetEnv(t, "GROK_AUTH_KEY_MISS_MAX_CONCURRENT")
	unsetEnv(t, "GROK_MAX_API_KEYS_PER_USER")
	unsetEnv(t, "GROK_BOOTSTRAP_CREDENTIALS_PATH")
	unsetEnv(t, "GROK_AUTH_USER_RPM_MAX_ENTRIES")
	unsetEnv(t, "GROK_AUTH_USER_RPM_FALLBACK_BUCKETS")
	unsetEnv(t, "GROK_SEARCH_MCP_DEBUG")
	unsetEnv(t, "GROK_SEARCH_MCP_IP_RPM")
	unsetEnv(t, "GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD")
	unsetEnv(t, "GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD")
	unsetEnv(t, "GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY")
	unsetEnv(t, "GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY")
	unsetEnv(t, "GROK_MCP_DEBUG")
	unsetEnv(t, "GROK_MCP_IP_RPM")
	unsetEnv(t, "GROK_MCP_GLOBAL_SEARCH_CONCURRENCY")
	unsetEnv(t, "GROK_MCP_USER_SEARCH_CONCURRENCY")
	setEnv(t, "CPA_API_KEY", "test-key")
	setEnv(t, "GROK_JWT_SECRET", "jwt-secret-must-be-at-least-32-bytes!")
}

func TestLoadAllowsMissingAPIKeyForDatabaseFallback(t *testing.T) {
	setEnv(t, "CPA_API_KEY", "")
	setEnv(t, "GROK_JWT_SECRET", "jwt-secret-must-be-at-least-32-bytes!")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load should allow a database to provide the CPA key: %v", err)
	}
	if cfg.CPAAPIKey != "" {
		t.Fatalf("expected empty environment CPA key, got %q", cfg.CPAAPIKey)
	}
}

func TestLoadDefaults(t *testing.T) {
	panelEnv(t)
	setEnv(t, "CPA_BASE_URL", "")
	setEnv(t, "GROK_UPSTREAM_PROTOCOL", "")
	setEnv(t, "GROK_MODEL", "")
	setEnv(t, "GROK_HTTP_TIMEOUT", "")
	setEnv(t, "GROK_SEARCH_MCP_DEBUG", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CPABaseURL != "http://127.0.0.1:8317" {
		t.Fatalf("unexpected base URL: %s", cfg.CPABaseURL)
	}
	if cfg.UpstreamProtocol != UpstreamProtocolResponses {
		t.Fatalf("unexpected upstream protocol: %s", cfg.UpstreamProtocol)
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
	if cfg.ClientIPMode != ratelimit.ClientIPModeDirect || len(cfg.TrustedProxyCIDRs) != 0 {
		t.Fatalf("unexpected client IP defaults: mode=%q prefixes=%v", cfg.ClientIPMode, cfg.TrustedProxyCIDRs)
	}
	if cfg.RegistrationMode != store.RegistrationModeDisabled {
		t.Fatalf("registration mode = %q, want %q", cfg.RegistrationMode, store.RegistrationModeDisabled)
	}
	if cfg.MaxAPIKeysPerUser != 20 {
		t.Fatalf("maximum API keys per user = %d, want 20", cfg.MaxAPIKeysPerUser)
	}
	if cfg.BootstrapCredentialsPath != cfg.DBPath+".bootstrap-admin" {
		t.Fatalf("bootstrap credentials path = %q", cfg.BootstrapCredentialsPath)
	}
}

func TestLoadTrustedProxyNetworkConfiguration(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_CLIENT_IP_MODE", " trusted_proxy ")
	setEnv(t, "GROK_TRUSTED_PROXY_CIDRS", "192.0.2.17/24, 2001:db8:1234::1/48, ::ffff:198.51.100.20/120")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ClientIPMode != ratelimit.ClientIPModeTrustedProxy {
		t.Fatalf("client IP mode = %q, want %q", cfg.ClientIPMode, ratelimit.ClientIPModeTrustedProxy)
	}
	actualPrefixes := make([]string, 0, len(cfg.TrustedProxyCIDRs))
	for _, prefix := range cfg.TrustedProxyCIDRs {
		actualPrefixes = append(actualPrefixes, prefix.String())
	}
	expectedPrefixes := []string{"192.0.2.0/24", "2001:db8:1234::/48", "198.51.100.0/24"}
	if strings.Join(actualPrefixes, ",") != strings.Join(expectedPrefixes, ",") {
		t.Fatalf("trusted proxy prefixes = %v, want %v", actualPrefixes, expectedPrefixes)
	}
}

func TestLoadRejectsInvalidNetworkTrustConfiguration(t *testing.T) {
	testCases := []struct {
		name          string
		mode          string
		trustedCIDRs  string
		expectedError string
	}{
		{name: "unknown mode", mode: "forwarded", expectedError: "GROK_CLIENT_IP_MODE must be"},
		{name: "trusted mode requires prefixes", mode: "trusted_proxy", expectedError: "GROK_TRUSTED_PROXY_CIDRS is required"},
		{name: "malformed prefix in direct mode", mode: "direct", trustedCIDRs: "not-a-prefix", expectedError: "invalid prefix"},
		{name: "empty list element", mode: "trusted_proxy", trustedCIDRs: "192.0.2.0/24,,2001:db8::/32", expectedError: "empty list elements"},
		{name: "zoned address", mode: "trusted_proxy", trustedCIDRs: "fe80::1%eth0/64", expectedError: "invalid prefix"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			panelEnv(t)
			setEnv(t, "GROK_CLIENT_IP_MODE", testCase.mode)
			setEnv(t, "GROK_TRUSTED_PROXY_CIDRS", testCase.trustedCIDRs)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), testCase.expectedError) {
				t.Fatalf("expected error containing %q, got %v", testCase.expectedError, err)
			}
		})
	}
}

func TestLoadRejectsExcessiveTrustedProxyPrefixes(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_CLIENT_IP_MODE", "trusted_proxy")
	trustedPrefixes := make([]string, maximumTrustedProxyPrefixes+1)
	for prefixIndex := range trustedPrefixes {
		trustedPrefixes[prefixIndex] = "192.0.2.0/24"
	}
	setEnv(t, "GROK_TRUSTED_PROXY_CIDRS", strings.Join(trustedPrefixes, ","))

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "at most 256 prefixes") {
		t.Fatalf("expected excessive-prefix error, got %v", err)
	}
}

func TestLoadInitialRegistrationModes(t *testing.T) {
	for _, expectedMode := range []store.RegistrationMode{
		store.RegistrationModeDisabled,
		store.RegistrationModeInvite,
		store.RegistrationModeFree,
	} {
		t.Run(string(expectedMode), func(t *testing.T) {
			panelEnv(t)
			setEnv(t, "GROK_INITIAL_REGISTRATION_MODE", string(expectedMode))
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load failed: %v", err)
			}
			if cfg.RegistrationMode != expectedMode {
				t.Fatalf("registration mode = %q, want %q", cfg.RegistrationMode, expectedMode)
			}
		})
	}

	panelEnv(t)
	setEnv(t, "GROK_INITIAL_REGISTRATION_MODE", "open")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "GROK_INITIAL_REGISTRATION_MODE") {
		t.Fatalf("expected initial registration mode error, got %v", err)
	}
}

func TestNormalizeUpstreamProtocol(t *testing.T) {
	testCases := []struct {
		name         string
		input        UpstreamProtocol
		expected     UpstreamProtocol
		expectsError bool
	}{
		{name: "rejects empty protocol", input: "", expectsError: true},
		{name: "trims and normalizes", input: " Chat_Completions ", expected: UpstreamProtocolChatCompletions},
		{name: "anthropic messages", input: UpstreamProtocolAnthropicMessages, expected: UpstreamProtocolAnthropicMessages},
		{name: "rejects unknown protocol", input: "legacy", expectsError: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := NormalizeUpstreamProtocol(testCase.input)
			if testCase.expectsError {
				if err == nil {
					t.Fatalf("expected protocol validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize protocol: %v", err)
			}
			if actual != testCase.expected {
				t.Fatalf("expected %q, got %q", testCase.expected, actual)
			}
		})
	}
}

func TestLoadDoesNotEnableProxyFromURLAlone(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_PROXY_URL", " http://127.0.0.1:7890 ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ProxyEnabled {
		t.Fatalf("expected proxy disabled without GROK_PROXY_ENABLED")
	}
	if cfg.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected proxy URL: %q", cfg.ProxyURL)
	}
}

func TestLoadHonorsExplicitProxyEnabledFlag(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_PROXY_URL", "http://127.0.0.1:7890")
	setEnv(t, "GROK_PROXY_ENABLED", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ProxyEnabled {
		t.Fatalf("expected GROK_PROXY_ENABLED=0 to disable explicit proxy")
	}
	if cfg.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected proxy URL: %q", cfg.ProxyURL)
	}
}

func TestLoadRejectsEnabledProxyWithoutURL(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_PROXY_ENABLED", "true")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "GROK_PROXY_URL is required when proxy is enabled") {
		t.Fatalf("expected proxy URL validation error, got %v", err)
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
		setEnv(t, "GROK_SEARCH_MCP_DEBUG", value)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load failed for %q: %v", value, err)
		}
		if !cfg.Debug {
			t.Fatalf("expected debug enabled for %q", value)
		}
	}

	setEnv(t, "GROK_SEARCH_MCP_DEBUG", "0")
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
	if cfg.HTTPAddr != ":8080" ||
		cfg.DBPath != "./grok-search-mcp.db" ||
		cfg.MCPIPRPM != 300 ||
		cfg.MCPIPMaxEntriesPerShard != 2048 ||
		cfg.MCPIPFallbackBucketsPerShard != 16 ||
		cfg.MCPGlobalSearchConcurrency != 16 ||
		cfg.MCPUserSearchConcurrency != 4 ||
		cfg.AuthPasswordMaxConcurrent != 4 ||
		cfg.AuthKeyMissMaxConcurrent != 32 ||
		cfg.UserRPMMaximumEntries != 16384 ||
		cfg.UserRPMFallbackBuckets != 64 {
		t.Fatalf("unexpected http defaults: %+v", cfg)
	}
}

func TestLoadBoundedAuthenticationWorkConfiguration(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_AUTH_PASSWORD_MAX_CONCURRENT", "7")
	setEnv(t, "GROK_AUTH_KEY_MISS_MAX_CONCURRENT", "41")
	setEnv(t, "GROK_MAX_API_KEYS_PER_USER", "37")
	setEnv(t, "GROK_BOOTSTRAP_CREDENTIALS_PATH", " /run/secrets/bootstrap.json ")
	setEnv(t, "GROK_AUTH_USER_RPM_MAX_ENTRIES", "1234")
	setEnv(t, "GROK_AUTH_USER_RPM_FALLBACK_BUCKETS", "19")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthPasswordMaxConcurrent != 7 ||
		cfg.AuthKeyMissMaxConcurrent != 41 ||
		cfg.MaxAPIKeysPerUser != 37 ||
		cfg.UserRPMMaximumEntries != 1234 ||
		cfg.UserRPMFallbackBuckets != 19 {
		t.Fatalf("authentication work configuration = %+v", cfg)
	}
	if cfg.BootstrapCredentialsPath != "/run/secrets/bootstrap.json" {
		t.Fatalf("bootstrap credentials path = %q", cfg.BootstrapCredentialsPath)
	}
}

func TestLoadRejectsInvalidBoundedAuthenticationWorkConfiguration(t *testing.T) {
	testCases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "password zero", key: "GROK_AUTH_PASSWORD_MAX_CONCURRENT", value: "0"},
		{name: "password above maximum", key: "GROK_AUTH_PASSWORD_MAX_CONCURRENT", value: "65"},
		{name: "key misses negative", key: "GROK_AUTH_KEY_MISS_MAX_CONCURRENT", value: "-1"},
		{name: "key misses above maximum", key: "GROK_AUTH_KEY_MISS_MAX_CONCURRENT", value: "1025"},
		{name: "API key limit zero", key: "GROK_MAX_API_KEYS_PER_USER", value: "0"},
		{name: "API key limit above maximum", key: "GROK_MAX_API_KEYS_PER_USER", value: "1001"},
		{name: "user entries above maximum", key: "GROK_AUTH_USER_RPM_MAX_ENTRIES", value: "65537"},
		{name: "fallback buckets above maximum", key: "GROK_AUTH_USER_RPM_FALLBACK_BUCKETS", value: "1025"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			panelEnv(t)
			setEnv(t, testCase.key, testCase.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), testCase.key) {
				t.Fatalf("expected %s validation error, got %v", testCase.key, err)
			}
		})
	}
}

func TestLoadUsageRetentionDefaults(t *testing.T) {
	panelEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.UsageRawRetention != 7*24*time.Hour {
		t.Fatalf("raw retention = %v, want 7 days", cfg.UsageRawRetention)
	}
	if cfg.UsageHourlyRetention != 90*24*time.Hour {
		t.Fatalf("hourly retention = %v, want 90 days", cfg.UsageHourlyRetention)
	}
	if cfg.UsageDailyRetention != 730*24*time.Hour {
		t.Fatalf("daily retention = %v, want 730 days", cfg.UsageDailyRetention)
	}
	if cfg.UsageMaintenanceInterval != time.Hour {
		t.Fatalf("maintenance interval = %v, want 1 hour", cfg.UsageMaintenanceInterval)
	}
}

func TestLoadCustomUsageRetention(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_USAGE_RAW_RETENTION_DAYS", "3")
	setEnv(t, "GROK_USAGE_HOURLY_RETENTION_DAYS", "30")
	setEnv(t, "GROK_USAGE_DAILY_RETENTION_DAYS", "365")
	setEnv(t, "GROK_USAGE_MAINTENANCE_INTERVAL", "30m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.UsageRawRetention != 3*24*time.Hour ||
		cfg.UsageHourlyRetention != 30*24*time.Hour ||
		cfg.UsageDailyRetention != 365*24*time.Hour ||
		cfg.UsageMaintenanceInterval != 30*time.Minute {
		t.Fatalf("unexpected usage maintenance config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidUsageRetention(t *testing.T) {
	testCases := []struct {
		name          string
		environment   map[string]string
		expectedError string
	}{
		{
			name:          "non-positive raw retention",
			environment:   map[string]string{"GROK_USAGE_RAW_RETENTION_DAYS": "0"},
			expectedError: "GROK_USAGE_RAW_RETENTION_DAYS must be a positive integer",
		},
		{
			name: "hourly retention must exceed raw retention",
			environment: map[string]string{
				"GROK_USAGE_RAW_RETENTION_DAYS":    "30",
				"GROK_USAGE_HOURLY_RETENTION_DAYS": "30",
			},
			expectedError: "GROK_USAGE_HOURLY_RETENTION_DAYS must exceed",
		},
		{
			name:          "invalid maintenance interval",
			environment:   map[string]string{"GROK_USAGE_MAINTENANCE_INTERVAL": "soon"},
			expectedError: "GROK_USAGE_MAINTENANCE_INTERVAL must be a positive duration",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			panelEnv(t)
			for environmentVariable, value := range testCase.environment {
				setEnv(t, environmentVariable, value)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), testCase.expectedError) {
				t.Fatalf("expected error containing %q, got %v", testCase.expectedError, err)
			}
		})
	}
}

func TestLoadCustomSecuritySettings(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_SEARCH_MCP_IP_RPM", "123")
	setEnv(t, "GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD", "321")
	setEnv(t, "GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD", "7")
	setEnv(t, "GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY", "12")
	setEnv(t, "GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.MCPIPRPM != 123 ||
		cfg.MCPIPMaxEntriesPerShard != 321 ||
		cfg.MCPIPFallbackBucketsPerShard != 7 ||
		cfg.MCPGlobalSearchConcurrency != 12 ||
		cfg.MCPUserSearchConcurrency != 3 {
		t.Fatalf("unexpected security settings: %+v", cfg)
	}
}

func TestLoadSupportsLegacyMCPEnvironmentVariableAliases(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_MCP_DEBUG", "true")
	setEnv(t, "GROK_MCP_IP_RPM", "123")
	setEnv(t, "GROK_MCP_GLOBAL_SEARCH_CONCURRENCY", "12")
	setEnv(t, "GROK_MCP_USER_SEARCH_CONCURRENCY", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.Debug ||
		cfg.MCPIPRPM != 123 ||
		cfg.MCPGlobalSearchConcurrency != 12 ||
		cfg.MCPUserSearchConcurrency != 3 {
		t.Fatalf("legacy aliases were not applied: %+v", cfg)
	}
}

func TestLoadPrefersRenamedMCPEnvironmentVariables(t *testing.T) {
	panelEnv(t)
	setEnv(t, "GROK_MCP_DEBUG", "true")
	setEnv(t, "GROK_MCP_IP_RPM", "999")
	setEnv(t, "GROK_MCP_GLOBAL_SEARCH_CONCURRENCY", "99")
	setEnv(t, "GROK_MCP_USER_SEARCH_CONCURRENCY", "9")
	setEnv(t, "GROK_SEARCH_MCP_DEBUG", "false")
	setEnv(t, "GROK_SEARCH_MCP_IP_RPM", "123")
	setEnv(t, "GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY", "12")
	setEnv(t, "GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Debug ||
		cfg.MCPIPRPM != 123 ||
		cfg.MCPGlobalSearchConcurrency != 12 ||
		cfg.MCPUserSearchConcurrency != 3 {
		t.Fatalf("renamed variables did not take precedence: %+v", cfg)
	}
}

func TestLoadRejectsInvalidSecuritySettings(t *testing.T) {
	testCases := []struct {
		name          string
		environment   map[string]string
		expectedError string
	}{
		{
			name:          "non-positive IP RPM",
			environment:   map[string]string{"GROK_SEARCH_MCP_IP_RPM": "0"},
			expectedError: "GROK_SEARCH_MCP_IP_RPM must be a positive integer",
		},
		{
			name:          "non-positive IP entries per shard",
			environment:   map[string]string{"GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD": "0"},
			expectedError: "GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD must be a positive integer",
		},
		{
			name:          "invalid IP fallback buckets per shard",
			environment:   map[string]string{"GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD": "many"},
			expectedError: "GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD must be a positive integer",
		},
		{
			name:          "IP entries per shard exceeds safe maximum",
			environment:   map[string]string{"GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD": "65537"},
			expectedError: "GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD must not exceed 65536",
		},
		{
			name:          "IP fallback buckets per shard exceeds safe maximum",
			environment:   map[string]string{"GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD": "1025"},
			expectedError: "GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD must not exceed 1024",
		},
		{
			name:          "non-positive global search concurrency",
			environment:   map[string]string{"GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY": "0"},
			expectedError: "GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY must be a positive integer",
		},
		{
			name:          "invalid user search concurrency",
			environment:   map[string]string{"GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY": "many"},
			expectedError: "GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY must be a positive integer",
		},
		{
			name: "user search concurrency exceeds global capacity",
			environment: map[string]string{
				"GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY": "2",
				"GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY":   "3",
			},
			expectedError: "GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY must not exceed GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			panelEnv(t)
			for environmentVariable, value := range testCase.environment {
				setEnv(t, environmentVariable, value)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), testCase.expectedError) {
				t.Fatalf("expected error containing %q, got %v", testCase.expectedError, err)
			}
		})
	}
}

func TestParseBoolEnvUnset(t *testing.T) {
	_ = os.Unsetenv("GROK_SEARCH_MCP_DEBUG")
	if parseBoolEnv("GROK_SEARCH_MCP_DEBUG") {
		t.Fatalf("expected false for unset env")
	}
}

func TestNormalizeServerSettingsValidatesSearchConcurrency(t *testing.T) {
	baseSettings := ServerSettings{
		CPABaseURL:                 "http://127.0.0.1:8317",
		CPAAPIKey:                  "test-key",
		UpstreamProtocol:           UpstreamProtocolResponses,
		Model:                      "grok-4.3",
		TimeoutSeconds:             120,
		MCPGlobalSearchConcurrency: 16,
		MCPUserSearchConcurrency:   4,
		RegistrationMode:           store.RegistrationModeFree,
	}

	testCases := []struct {
		name          string
		globalLimit   int
		perUserLimit  int
		expectedError string
	}{
		{name: "valid", globalLimit: 8, perUserLimit: 2},
		{name: "zero global", globalLimit: 0, perUserLimit: 1, expectedError: "GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY"},
		{name: "zero per-user", globalLimit: 8, perUserLimit: 0, expectedError: "GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY"},
		{name: "per-user exceeds global", globalLimit: 2, perUserLimit: 3, expectedError: "must not exceed"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			settings := baseSettings
			settings.MCPGlobalSearchConcurrency = testCase.globalLimit
			settings.MCPUserSearchConcurrency = testCase.perUserLimit
			normalizedSettings, err := NormalizeServerSettings(settings)
			if testCase.expectedError == "" {
				if err != nil {
					t.Fatalf("NormalizeServerSettings failed: %v", err)
				}
				if normalizedSettings.MCPGlobalSearchConcurrency != testCase.globalLimit || normalizedSettings.MCPUserSearchConcurrency != testCase.perUserLimit {
					t.Fatalf("normalized search concurrency = %+v", normalizedSettings)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.expectedError) {
				t.Fatalf("expected error containing %q, got %v", testCase.expectedError, err)
			}
		})
	}
}
