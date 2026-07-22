// Package config 从环境变量加载 grok-search-mcp 的运行时配置并做基本校验。
package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/settings"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

const (
	defaultBaseURL          = "http://127.0.0.1:8317"
	defaultModel            = "grok-4.3"
	defaultUpstreamProtocol = UpstreamProtocolResponses
	// defaultTimeout limits each upstream connection, TLS handshake, and
	// response-header phase. Active SSE bodies remain governed by caller context.
	defaultTimeout                  = 120 * time.Second
	defaultHTTPAddr                 = ":8080"
	defaultDBPath                   = "./grok-search-mcp.db"
	defaultUsageRawRetention        = 7 * 24 * time.Hour
	defaultUsageHourlyRetention     = 90 * 24 * time.Hour
	defaultUsageDailyRetention      = 730 * 24 * time.Hour
	defaultUsageMaintenanceInterval = time.Hour
	// defaultMCPIPRPM limits /mcp by the startup-selected client identity
	// before API-key authentication.
	defaultMCPIPRPM                     = 300
	defaultMCPIPMaxEntriesPerShard      = 2048
	defaultMCPIPFallbackBucketsPerShard = 16
	maximumMCPIPMaxEntriesPerShard      = 65536
	maximumMCPIPFallbackBucketsPerShard = 1024
	defaultMCPGlobalSearchConcurrency   = 16
	defaultMCPUserSearchConcurrency     = 4
	defaultAuthPasswordMaxConcurrent    = 4
	maximumAuthPasswordMaxConcurrent    = 64
	defaultAuthKeyMissMaxConcurrent     = 32
	maximumAuthKeyMissMaxConcurrent     = 1024
	defaultMaxAPIKeysPerUser            = 20
	maximumMaxAPIKeysPerUser            = 1000
	defaultUserRPMMaximumEntries        = 16384
	defaultUserRPMFallbackBuckets       = 64
	maximumUserRPMMaximumEntries        = 65536
	maximumUserRPMFallbackBuckets       = 1024
	maximumTrustedProxyPrefixes         = 256
)

// UpstreamProtocol identifies the CPA-compatible HTTP protocol used for
// search requests.
type UpstreamProtocol = settings.UpstreamProtocol

const (
	UpstreamProtocolResponses         = settings.UpstreamProtocolResponses
	UpstreamProtocolChatCompletions   = settings.UpstreamProtocolChatCompletions
	UpstreamProtocolAnthropicMessages = settings.UpstreamProtocolAnthropicMessages
)

// Config 保存进程启动所需的全部配置项。
type Config struct {
	CPABaseURL                   string
	CPAAPIKey                    string `json:"-"`
	UpstreamProtocol             UpstreamProtocol
	Model                        string
	Timeout                      time.Duration
	Debug                        bool
	HTTPAddr                     string
	DBPath                       string
	JWTSecret                    string `json:"-"`
	ClientIPMode                 ratelimit.ClientIPMode
	TrustedProxyCIDRs            []netip.Prefix
	MCPIPRPM                     int
	MCPIPMaxEntriesPerShard      int
	MCPIPFallbackBucketsPerShard int
	MCPGlobalSearchConcurrency   int
	MCPUserSearchConcurrency     int
	AuthPasswordMaxConcurrent    int
	AuthKeyMissMaxConcurrent     int
	MaxAPIKeysPerUser            int
	UserRPMMaximumEntries        int
	UserRPMFallbackBuckets       int
	UsageRawRetention            time.Duration
	UsageHourlyRetention         time.Duration
	UsageDailyRetention          time.Duration
	UsageMaintenanceInterval     time.Duration
	ProxyURL                     string
	ProxyEnabled                 bool
	RegistrationMode             store.RegistrationMode
	BootstrapCredentialsPath     string
}

// ServerSettings contains runtime-tunable settings exposed in the admin panel.
// It intentionally excludes listener address, database path, and JWT secret
// because changing those safely requires a process restart.
type ServerSettings = settings.Runtime

// Load 读取并校验配置。
func Load() (*Config, error) {
	proxyURL := strings.TrimSpace(os.Getenv("GROK_PROXY_URL"))
	clientIPMode, err := parseClientIPMode(os.Getenv("GROK_CLIENT_IP_MODE"))
	if err != nil {
		return nil, err
	}
	trustedProxyCIDRs, err := parseTrustedProxyCIDRs(os.Getenv("GROK_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return nil, err
	}
	if clientIPMode == ratelimit.ClientIPModeTrustedProxy && len(trustedProxyCIDRs) == 0 {
		return nil, fmt.Errorf("GROK_TRUSTED_PROXY_CIDRS is required in trusted_proxy mode")
	}
	initialRegistrationMode, err := parseInitialRegistrationMode(os.Getenv("GROK_INITIAL_REGISTRATION_MODE"))
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		CPABaseURL:                   strings.TrimRight(envOrDefault("CPA_BASE_URL", defaultBaseURL), "/"),
		CPAAPIKey:                    strings.TrimSpace(os.Getenv("CPA_API_KEY")),
		UpstreamProtocol:             UpstreamProtocol(envOrDefault("GROK_UPSTREAM_PROTOCOL", string(defaultUpstreamProtocol))),
		Model:                        envOrDefault("GROK_MODEL", defaultModel),
		Timeout:                      defaultTimeout,
		Debug:                        parseAliasedBoolEnvironmentVariable("GROK_SEARCH_MCP_DEBUG", "GROK_MCP_DEBUG"),
		HTTPAddr:                     envOrDefault("GROK_HTTP_ADDR", defaultHTTPAddr),
		DBPath:                       envOrDefault("GROK_DB_PATH", defaultDBPath),
		JWTSecret:                    strings.TrimSpace(os.Getenv("GROK_JWT_SECRET")),
		ClientIPMode:                 clientIPMode,
		TrustedProxyCIDRs:            trustedProxyCIDRs,
		MCPIPRPM:                     defaultMCPIPRPM,
		MCPIPMaxEntriesPerShard:      defaultMCPIPMaxEntriesPerShard,
		MCPIPFallbackBucketsPerShard: defaultMCPIPFallbackBucketsPerShard,
		MCPGlobalSearchConcurrency:   defaultMCPGlobalSearchConcurrency,
		MCPUserSearchConcurrency:     defaultMCPUserSearchConcurrency,
		AuthPasswordMaxConcurrent:    defaultAuthPasswordMaxConcurrent,
		AuthKeyMissMaxConcurrent:     defaultAuthKeyMissMaxConcurrent,
		MaxAPIKeysPerUser:            defaultMaxAPIKeysPerUser,
		UserRPMMaximumEntries:        defaultUserRPMMaximumEntries,
		UserRPMFallbackBuckets:       defaultUserRPMFallbackBuckets,
		UsageRawRetention:            defaultUsageRawRetention,
		UsageHourlyRetention:         defaultUsageHourlyRetention,
		UsageDailyRetention:          defaultUsageDailyRetention,
		UsageMaintenanceInterval:     defaultUsageMaintenanceInterval,
		ProxyURL:                     proxyURL,
		ProxyEnabled:                 parseBoolEnv("GROK_PROXY_ENABLED"),
		RegistrationMode:             initialRegistrationMode,
	}
	cfg.BootstrapCredentialsPath = strings.TrimSpace(os.Getenv("GROK_BOOTSTRAP_CREDENTIALS_PATH"))
	if cfg.BootstrapCredentialsPath == "" {
		cfg.BootstrapCredentialsPath = cfg.DBPath + ".bootstrap-admin"
	}

	timeoutSeconds, err := parsePositiveIntegerEnvironmentVariable(
		"GROK_HTTP_TIMEOUT",
		int(defaultTimeout/time.Second),
		" (seconds)",
	)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = time.Duration(timeoutSeconds) * time.Second

	cfg.MCPIPRPM, err = parseAliasedPositiveIntegerEnvironmentVariable(
		"GROK_SEARCH_MCP_IP_RPM",
		"GROK_MCP_IP_RPM",
		defaultMCPIPRPM,
		"",
	)
	if err != nil {
		return nil, err
	}
	cfg.MCPIPMaxEntriesPerShard, err = parsePositiveIntegerEnvironmentVariable(
		"GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD",
		defaultMCPIPMaxEntriesPerShard,
		"",
	)
	if err != nil {
		return nil, err
	}
	if cfg.MCPIPMaxEntriesPerShard > maximumMCPIPMaxEntriesPerShard {
		return nil, fmt.Errorf(
			"GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD must not exceed %d, got %d",
			maximumMCPIPMaxEntriesPerShard,
			cfg.MCPIPMaxEntriesPerShard,
		)
	}
	cfg.MCPIPFallbackBucketsPerShard, err = parsePositiveIntegerEnvironmentVariable(
		"GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD",
		defaultMCPIPFallbackBucketsPerShard,
		"",
	)
	if err != nil {
		return nil, err
	}
	if cfg.MCPIPFallbackBucketsPerShard > maximumMCPIPFallbackBucketsPerShard {
		return nil, fmt.Errorf(
			"GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD must not exceed %d, got %d",
			maximumMCPIPFallbackBucketsPerShard,
			cfg.MCPIPFallbackBucketsPerShard,
		)
	}

	cfg.MCPGlobalSearchConcurrency, err = parseAliasedPositiveIntegerEnvironmentVariable(
		"GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY",
		"GROK_MCP_GLOBAL_SEARCH_CONCURRENCY",
		defaultMCPGlobalSearchConcurrency,
		"",
	)
	if err != nil {
		return nil, err
	}
	cfg.MCPUserSearchConcurrency, err = parseAliasedPositiveIntegerEnvironmentVariable(
		"GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY",
		"GROK_MCP_USER_SEARCH_CONCURRENCY",
		defaultMCPUserSearchConcurrency,
		"",
	)
	if err != nil {
		return nil, err
	}
	if cfg.MCPUserSearchConcurrency > cfg.MCPGlobalSearchConcurrency {
		return nil, fmt.Errorf("GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY must not exceed GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY")
	}

	cfg.AuthPasswordMaxConcurrent, err = parseBoundedPositiveIntegerEnvironmentVariable(
		"GROK_AUTH_PASSWORD_MAX_CONCURRENT",
		defaultAuthPasswordMaxConcurrent,
		maximumAuthPasswordMaxConcurrent,
	)
	if err != nil {
		return nil, err
	}
	cfg.AuthKeyMissMaxConcurrent, err = parseBoundedPositiveIntegerEnvironmentVariable(
		"GROK_AUTH_KEY_MISS_MAX_CONCURRENT",
		defaultAuthKeyMissMaxConcurrent,
		maximumAuthKeyMissMaxConcurrent,
	)
	if err != nil {
		return nil, err
	}
	cfg.MaxAPIKeysPerUser, err = parseBoundedPositiveIntegerEnvironmentVariable(
		"GROK_MAX_API_KEYS_PER_USER",
		defaultMaxAPIKeysPerUser,
		maximumMaxAPIKeysPerUser,
	)
	if err != nil {
		return nil, err
	}
	cfg.UserRPMMaximumEntries, err = parseBoundedPositiveIntegerEnvironmentVariable(
		"GROK_AUTH_USER_RPM_MAX_ENTRIES",
		defaultUserRPMMaximumEntries,
		maximumUserRPMMaximumEntries,
	)
	if err != nil {
		return nil, err
	}
	cfg.UserRPMFallbackBuckets, err = parseBoundedPositiveIntegerEnvironmentVariable(
		"GROK_AUTH_USER_RPM_FALLBACK_BUCKETS",
		defaultUserRPMFallbackBuckets,
		maximumUserRPMFallbackBuckets,
	)
	if err != nil {
		return nil, err
	}

	if cfg.UsageRawRetention, err = parseRetentionDays(
		"GROK_USAGE_RAW_RETENTION_DAYS",
		defaultUsageRawRetention,
	); err != nil {
		return nil, err
	}
	if cfg.UsageHourlyRetention, err = parseRetentionDays(
		"GROK_USAGE_HOURLY_RETENTION_DAYS",
		defaultUsageHourlyRetention,
	); err != nil {
		return nil, err
	}
	if cfg.UsageDailyRetention, err = parseRetentionDays(
		"GROK_USAGE_DAILY_RETENTION_DAYS",
		defaultUsageDailyRetention,
	); err != nil {
		return nil, err
	}
	if raw := strings.TrimSpace(os.Getenv("GROK_USAGE_MAINTENANCE_INTERVAL")); raw != "" {
		cfg.UsageMaintenanceInterval, err = time.ParseDuration(raw)
		if err != nil || cfg.UsageMaintenanceInterval <= 0 {
			return nil, fmt.Errorf("GROK_USAGE_MAINTENANCE_INTERVAL must be a positive duration, got %q", raw)
		}
	}
	if cfg.UsageHourlyRetention <= cfg.UsageRawRetention {
		return nil, fmt.Errorf("GROK_USAGE_HOURLY_RETENTION_DAYS must exceed GROK_USAGE_RAW_RETENTION_DAYS")
	}
	if cfg.UsageDailyRetention <= cfg.UsageHourlyRetention {
		return nil, fmt.Errorf("GROK_USAGE_DAILY_RETENTION_DAYS must exceed GROK_USAGE_HOURLY_RETENTION_DAYS")
	}

	// Validate and canonicalize environment defaults without requiring the CPA
	// key yet. An existing database may provide the complete runtime settings.
	environmentSettings, err := normalizeServerSettings(cfg.ServerSettings(), false)
	if err != nil {
		return nil, err
	}
	cfg.CPABaseURL = environmentSettings.CPABaseURL
	cfg.CPAAPIKey = environmentSettings.CPAAPIKey
	cfg.UpstreamProtocol = environmentSettings.UpstreamProtocol
	cfg.Model = environmentSettings.Model
	cfg.Timeout = time.Duration(environmentSettings.TimeoutSeconds) * time.Second
	cfg.ProxyURL = environmentSettings.ProxyURL
	cfg.ProxyEnabled = environmentSettings.ProxyEnabled
	cfg.RegistrationMode = environmentSettings.RegistrationMode
	cfg.Debug = environmentSettings.Debug

	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("GROK_JWT_SECRET is required")
	}
	// HS256 的安全性依赖密钥长度；短密钥可被离线暴力破解伪造 token。
	// RFC 7518 推荐 HS256 使用至少 256 位（32 字节）密钥，此处据此拒绝弱密钥。
	const minJWTSecretLen = 32
	if len(cfg.JWTSecret) < minJWTSecretLen {
		return nil, fmt.Errorf("GROK_JWT_SECRET must be at least %d bytes to avoid weak-key attacks on HS256", minJWTSecretLen)
	}

	return cfg, nil
}

func parseClientIPMode(rawMode string) (ratelimit.ClientIPMode, error) {
	normalizedMode := ratelimit.ClientIPMode(strings.ToLower(strings.TrimSpace(rawMode)))
	if normalizedMode == "" {
		return ratelimit.ClientIPModeDirect, nil
	}
	switch normalizedMode {
	case ratelimit.ClientIPModeDirect, ratelimit.ClientIPModeTrustedProxy:
		return normalizedMode, nil
	default:
		return "", fmt.Errorf(
			"GROK_CLIENT_IP_MODE must be %q or %q, got %q",
			ratelimit.ClientIPModeDirect,
			ratelimit.ClientIPModeTrustedProxy,
			rawMode,
		)
	}
}

func parseTrustedProxyCIDRs(rawCIDRs string) ([]netip.Prefix, error) {
	if strings.TrimSpace(rawCIDRs) == "" {
		return nil, nil
	}

	rawPrefixes := strings.Split(rawCIDRs, ",")
	if len(rawPrefixes) > maximumTrustedProxyPrefixes {
		return nil, fmt.Errorf(
			"GROK_TRUSTED_PROXY_CIDRS must contain at most %d prefixes",
			maximumTrustedProxyPrefixes,
		)
	}

	trustedProxyCIDRs := make([]netip.Prefix, 0, len(rawPrefixes))
	for _, rawPrefix := range rawPrefixes {
		prefixText := strings.TrimSpace(rawPrefix)
		if prefixText == "" {
			return nil, fmt.Errorf("GROK_TRUSTED_PROXY_CIDRS must not contain empty list elements")
		}
		prefix, err := netip.ParsePrefix(prefixText)
		if err != nil || !prefix.IsValid() || prefix.Addr().Zone() != "" {
			return nil, fmt.Errorf("GROK_TRUSTED_PROXY_CIDRS contains invalid prefix %q", prefixText)
		}
		canonicalPrefix, err := canonicalizeTrustedProxyPrefix(prefix)
		if err != nil {
			return nil, fmt.Errorf("GROK_TRUSTED_PROXY_CIDRS contains invalid prefix %q: %w", prefixText, err)
		}
		trustedProxyCIDRs = append(trustedProxyCIDRs, canonicalPrefix)
	}
	return trustedProxyCIDRs, nil
}

func canonicalizeTrustedProxyPrefix(prefix netip.Prefix) (netip.Prefix, error) {
	if !prefix.Addr().Is4In6() {
		return prefix.Masked(), nil
	}
	if prefix.Bits() < 96 {
		return netip.Prefix{}, fmt.Errorf("mapped IPv6 prefixes must have at least 96 prefix bits")
	}
	return netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96).Masked(), nil
}

func parseInitialRegistrationMode(rawMode string) (store.RegistrationMode, error) {
	normalizedMode := store.RegistrationMode(strings.ToLower(strings.TrimSpace(rawMode)))
	if normalizedMode == "" {
		return store.RegistrationModeDisabled, nil
	}
	registrationMode, err := store.NormalizeRegistrationMode(normalizedMode)
	if err != nil {
		return "", fmt.Errorf("GROK_INITIAL_REGISTRATION_MODE must be disabled, invite, or free, got %q", rawMode)
	}
	return registrationMode, nil
}

// ServerSettings returns the current runtime-tunable server settings.
func (c *Config) ServerSettings() ServerSettings {
	timeoutSeconds := int(c.Timeout / time.Second)
	if timeoutSeconds <= 0 {
		timeoutSeconds = int(defaultTimeout / time.Second)
	}
	return ServerSettings{
		CPABaseURL:                 c.CPABaseURL,
		CPAAPIKey:                  c.CPAAPIKey,
		UpstreamProtocol:           c.UpstreamProtocol,
		Model:                      c.Model,
		TimeoutSeconds:             timeoutSeconds,
		MCPGlobalSearchConcurrency: c.MCPGlobalSearchConcurrency,
		MCPUserSearchConcurrency:   c.MCPUserSearchConcurrency,
		ProxyURL:                   c.ProxyURL,
		ProxyEnabled:               c.ProxyEnabled,
		RegistrationMode:           c.RegistrationMode,
		Debug:                      c.Debug,
	}
}

// NormalizeServerSettings trims, validates, and canonicalizes settings that can
// be edited from the admin panel.
func NormalizeServerSettings(settings ServerSettings) (ServerSettings, error) {
	return normalizeServerSettings(settings, true)
}

func normalizeServerSettings(settings ServerSettings, requireAPIKey bool) (ServerSettings, error) {
	settings.CPABaseURL = strings.TrimRight(strings.TrimSpace(settings.CPABaseURL), "/")
	settings.CPAAPIKey = strings.TrimSpace(settings.CPAAPIKey)
	upstreamProtocol, err := NormalizeUpstreamProtocol(settings.UpstreamProtocol)
	if err != nil {
		return settings, err
	}
	settings.UpstreamProtocol = upstreamProtocol
	settings.Model = strings.TrimSpace(settings.Model)
	settings.ProxyURL = strings.TrimSpace(settings.ProxyURL)
	registrationMode, err := store.NormalizeRegistrationMode(settings.RegistrationMode)
	if err != nil {
		return settings, err
	}
	settings.RegistrationMode = registrationMode

	if requireAPIKey && settings.CPAAPIKey == "" {
		return settings, fmt.Errorf("CPA_API_KEY is required")
	}
	if settings.CPABaseURL == "" {
		return settings, fmt.Errorf("CPA_BASE_URL must not be empty")
	}
	if err := validateHTTPURL("CPA_BASE_URL", settings.CPABaseURL); err != nil {
		return settings, err
	}
	if settings.Model == "" {
		return settings, fmt.Errorf("GROK_MODEL must not be empty")
	}
	if err := ValidateModel(settings.Model); err != nil {
		return settings, err
	}
	if settings.TimeoutSeconds <= 0 {
		return settings, fmt.Errorf("GROK_HTTP_TIMEOUT must be a positive integer (seconds), got %d", settings.TimeoutSeconds)
	}
	if err := validateSearchConcurrencyLimits(settings.MCPGlobalSearchConcurrency, settings.MCPUserSearchConcurrency); err != nil {
		return settings, err
	}
	if settings.ProxyEnabled {
		if settings.ProxyURL == "" {
			return settings, fmt.Errorf("GROK_PROXY_URL is required when proxy is enabled")
		}
		if err := validateHTTPURL("GROK_PROXY_URL", settings.ProxyURL); err != nil {
			return settings, err
		}
	}

	return settings, nil
}

func validateSearchConcurrencyLimits(globalLimit, perUserLimit int) error {
	if globalLimit <= 0 {
		return fmt.Errorf("GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY must be a positive integer, got %d", globalLimit)
	}
	if perUserLimit <= 0 {
		return fmt.Errorf("GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY must be a positive integer, got %d", perUserLimit)
	}
	if perUserLimit > globalLimit {
		return fmt.Errorf("GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY must not exceed GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY")
	}
	return nil
}

// NormalizeUpstreamProtocol canonicalizes an explicitly configured protocol.
func NormalizeUpstreamProtocol(protocol UpstreamProtocol) (UpstreamProtocol, error) {
	normalizedProtocol := UpstreamProtocol(strings.ToLower(strings.TrimSpace(string(protocol))))
	switch normalizedProtocol {
	case UpstreamProtocolResponses:
		return UpstreamProtocolResponses, nil
	case UpstreamProtocolChatCompletions:
		return UpstreamProtocolChatCompletions, nil
	case UpstreamProtocolAnthropicMessages:
		return UpstreamProtocolAnthropicMessages, nil
	default:
		return "", fmt.Errorf(
			"upstream_protocol must be one of %q, %q, or %q",
			UpstreamProtocolResponses,
			UpstreamProtocolChatCompletions,
			UpstreamProtocolAnthropicMessages,
		)
	}
}

// ValidateModel 校验模型名是否合法：只需包含 "grok"（不区分大小写）即可。
// 供 config.NormalizeServerSettings 与 grok.validateModel 共享同一规则，
// 避免面板保存的模型名在请求时被 grok 层拒绝导致全部搜索不可用。
func ValidateModel(model string) error {
	if !strings.Contains(strings.ToLower(model), "grok") {
		return fmt.Errorf("unsupported model (must contain 'grok')")
	}
	return nil
}

func validateHTTPURL(name, rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return fmt.Errorf("%s must be a valid http(s) URL", name)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("%s must use http or https", name)
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func parseBoolEnv(key string) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	return raw == "1" || raw == "true" || raw == "yes"
}

func parseAliasedBoolEnvironmentVariable(primaryEnvironmentVariable, legacyEnvironmentVariable string) bool {
	rawValue, _ := aliasedEnvironmentVariableValue(primaryEnvironmentVariable, legacyEnvironmentVariable)
	normalizedValue := strings.ToLower(rawValue)
	return normalizedValue == "1" || normalizedValue == "true" || normalizedValue == "yes"
}

func parseAliasedPositiveIntegerEnvironmentVariable(
	primaryEnvironmentVariable string,
	legacyEnvironmentVariable string,
	defaultValue int,
	errorSuffix string,
) (int, error) {
	rawValue, configuredEnvironmentVariable := aliasedEnvironmentVariableValue(
		primaryEnvironmentVariable,
		legacyEnvironmentVariable,
	)
	if rawValue == "" {
		return defaultValue, nil
	}

	parsedValue, err := strconv.Atoi(rawValue)
	if err != nil || parsedValue <= 0 {
		return 0, fmt.Errorf(
			"%s must be a positive integer%s, got %q",
			configuredEnvironmentVariable,
			errorSuffix,
			rawValue,
		)
	}

	return parsedValue, nil
}

func aliasedEnvironmentVariableValue(primaryEnvironmentVariable, legacyEnvironmentVariable string) (string, string) {
	if primaryValue, primaryIsConfigured := os.LookupEnv(primaryEnvironmentVariable); primaryIsConfigured {
		return strings.TrimSpace(primaryValue), primaryEnvironmentVariable
	}
	if legacyValue, legacyIsConfigured := os.LookupEnv(legacyEnvironmentVariable); legacyIsConfigured {
		return strings.TrimSpace(legacyValue), legacyEnvironmentVariable
	}
	return "", primaryEnvironmentVariable
}

func parsePositiveIntegerEnvironmentVariable(
	environmentVariable string,
	defaultValue int,
	errorSuffix string,
) (int, error) {
	rawValue := strings.TrimSpace(os.Getenv(environmentVariable))
	if rawValue == "" {
		return defaultValue, nil
	}

	parsedValue, err := strconv.Atoi(rawValue)
	if err != nil || parsedValue <= 0 {
		return 0, fmt.Errorf(
			"%s must be a positive integer%s, got %q",
			environmentVariable,
			errorSuffix,
			rawValue,
		)
	}

	return parsedValue, nil
}

func parseBoundedPositiveIntegerEnvironmentVariable(
	environmentVariable string,
	defaultValue int,
	maximumValue int,
) (int, error) {
	parsedValue, err := parsePositiveIntegerEnvironmentVariable(environmentVariable, defaultValue, "")
	if err != nil {
		return 0, err
	}
	if parsedValue > maximumValue {
		return 0, fmt.Errorf(
			"%s must not exceed %d, got %d",
			environmentVariable,
			maximumValue,
			parsedValue,
		)
	}
	return parsedValue, nil
}

func parseRetentionDays(environmentVariable string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(environmentVariable))
	if raw == "" {
		return fallback, nil
	}

	days, err := strconv.Atoi(raw)
	if err != nil || days <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer (days), got %q", environmentVariable, raw)
	}
	const maximumRetentionDays = 36500
	if days > maximumRetentionDays {
		return 0, fmt.Errorf("%s must not exceed %d days, got %q", environmentVariable, maximumRetentionDays, raw)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}
