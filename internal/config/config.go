// Package config 从环境变量加载 grok-mcp 的运行时配置并做基本校验。
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/grok-mcp/internal/store"
)

const (
	defaultBaseURL  = "http://127.0.0.1:8317"
	defaultModel    = "grok-4.3"
	defaultTimeout  = 120 * time.Second
	defaultHTTPAddr = ":8080"
	defaultDBPath   = "./grok-mcp.db"
	// defaultMCPIPRPM 在 API key 鉴权前按来源 IP 限制 /mcp 请求，保护认证存储免受暴力探测和 DoS。
	defaultMCPIPRPM = 300
)

// Config 保存进程启动所需的全部配置项。
type Config struct {
	CPABaseURL string
	CPAAPIKey  string
	Model      string
	Timeout    time.Duration
	Debug      bool
	HTTPAddr   string
	DBPath     string
	JWTSecret  string
	MCPIPRPM   int
	// TrustedProxies 为可信反向代理 CIDR；仅当 RemoteAddr 命中时才解析 X-Forwarded-For / X-Real-IP。
	// 空表示永不信任转发头（公网直连安全默认）。
	TrustedProxies   []*net.IPNet
	ProxyURL         string
	ProxyEnabled     bool
	RegistrationMode store.RegistrationMode
}

// ServerSettings contains the runtime-tunable upstream settings exposed in the
// admin panel. It intentionally excludes listener address, database path, and
// JWT secret because changing those safely requires a process restart.
type ServerSettings struct {
	CPABaseURL       string
	CPAAPIKey        string
	Model            string
	TimeoutSeconds   int
	ProxyURL         string
	ProxyEnabled     bool
	RegistrationMode store.RegistrationMode
	Debug            bool
}

// Load 读取并校验配置。
func Load() (*Config, error) {
	proxyURL := strings.TrimSpace(os.Getenv("GROK_PROXY_URL"))
	cfg := &Config{
		CPABaseURL:   strings.TrimRight(envOrDefault("CPA_BASE_URL", defaultBaseURL), "/"),
		CPAAPIKey:    strings.TrimSpace(os.Getenv("CPA_API_KEY")),
		Model:        envOrDefault("GROK_MODEL", defaultModel),
		Timeout:      defaultTimeout,
		Debug:        parseBoolEnv("GROK_MCP_DEBUG"),
		HTTPAddr:     envOrDefault("GROK_HTTP_ADDR", defaultHTTPAddr),
		DBPath:       envOrDefault("GROK_DB_PATH", defaultDBPath),
		JWTSecret:    strings.TrimSpace(os.Getenv("GROK_JWT_SECRET")),
		MCPIPRPM:     defaultMCPIPRPM,
		ProxyURL:     proxyURL,
		ProxyEnabled: resolveProxyEnabledFromEnv(proxyURL),
	}

	if raw := strings.TrimSpace(os.Getenv("GROK_HTTP_TIMEOUT")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf("GROK_HTTP_TIMEOUT must be a positive integer (seconds), got %q", raw)
		}
		cfg.Timeout = time.Duration(seconds) * time.Second
	}

	if raw := strings.TrimSpace(os.Getenv("GROK_MCP_IP_RPM")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("GROK_MCP_IP_RPM must be a positive integer, got %q", raw)
		}
		cfg.MCPIPRPM = n
	}

	// GROK_TRUSTED_PROXIES: 逗号分隔 CIDR 或单 IP（自动补 /32 或 /128）。
	// 仅列出边缘反代地址；空则 IP 限流只用 RemoteAddr。
	if raw := strings.TrimSpace(os.Getenv("GROK_TRUSTED_PROXIES")); raw != "" {
		networks, err := parseTrustedProxyCIDRs(raw)
		if err != nil {
			return nil, err
		}
		cfg.TrustedProxies = networks
	}

	// Validate and canonicalize environment defaults without requiring the CPA
	// key yet. An existing database may provide the complete runtime settings.
	environmentSettings, err := normalizeServerSettings(cfg.ServerSettings(), false)
	if err != nil {
		return nil, err
	}
	cfg.CPABaseURL = environmentSettings.CPABaseURL
	cfg.CPAAPIKey = environmentSettings.CPAAPIKey
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

// ServerSettings returns the current runtime-tunable server settings.
func (c *Config) ServerSettings() ServerSettings {
	timeoutSeconds := int(c.Timeout / time.Second)
	if timeoutSeconds <= 0 {
		timeoutSeconds = int(defaultTimeout / time.Second)
	}
	return ServerSettings{
		CPABaseURL:       c.CPABaseURL,
		CPAAPIKey:        c.CPAAPIKey,
		Model:            c.Model,
		TimeoutSeconds:   timeoutSeconds,
		ProxyURL:         c.ProxyURL,
		ProxyEnabled:     c.ProxyEnabled,
		RegistrationMode: c.RegistrationMode,
		Debug:            c.Debug,
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

// ValidateModel 校验模型名是否合法：只需包含 "grok"（不区分大小写）即可。
// 供 config.NormalizeServerSettings 与 grok.validateModel 共享同一规则，
// 避免面板保存的模型名在请求时被 grok 层拒绝导致全部搜索不可用。
func ValidateModel(model string) error {
	if !strings.Contains(strings.ToLower(model), "grok") {
		return fmt.Errorf("unsupported model: %q (must contain 'grok')", model)
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

func resolveProxyEnabledFromEnv(proxyURL string) bool {
	if raw, ok := os.LookupEnv("GROK_PROXY_ENABLED"); ok {
		normalizedRawValue := strings.TrimSpace(strings.ToLower(raw))
		return normalizedRawValue == "1" || normalizedRawValue == "true" || normalizedRawValue == "yes"
	}

	// Treat GROK_PROXY_URL by itself as an explicit proxy configuration. When it
	// is absent, the HTTP client falls back to standard HTTP_PROXY/HTTPS_PROXY
	// environment variables through net/http.
	return strings.TrimSpace(proxyURL) != ""
}

// parseTrustedProxyCIDRs 解析逗号分隔的 CIDR 或单 IP 列表。
func parseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	parts := strings.Split(raw, ",")
	networks := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("GROK_TRUSTED_PROXIES entry %q is not a valid IP or CIDR", entry)
			}
			if ip.To4() != nil {
				entry = entry + "/32"
			} else {
				entry = entry + "/128"
			}
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("GROK_TRUSTED_PROXIES entry %q: %w", strings.TrimSpace(part), err)
		}
		networks = append(networks, network)
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("GROK_TRUSTED_PROXIES must contain at least one IP or CIDR")
	}
	return networks, nil
}
