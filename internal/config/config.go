// Package config 从环境变量加载 grok-mcp 的运行时配置并做基本校验。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL  = "http://127.0.0.1:8317"
	defaultModel    = "grok-4.3"
	defaultTimeout  = 120 * time.Second
	defaultHTTPAddr = "127.0.0.1:8080"
	defaultDBPath   = "./grok-mcp.db"
	// defaultMCPIPRPM 在 API key 鉴权前按来源 IP 限制 /mcp 请求，保护认证存储免受暴力探测和 DoS。
	defaultMCPIPRPM = 300
	// defaultLimiterRPM 是内置内存限流器的兜底 RPM；限额实际取值始终来自 tier。
	defaultLimiterRPM = 60
)

const (
	// PanelRegistrationBootstrapOnly 仅允许空库初始化首个管理员账号；后续用户应由管理员创建。
	PanelRegistrationBootstrapOnly = "bootstrap-only"
	// PanelRegistrationOpen 保持传统自助注册行为，适合可信内网或测试环境。
	PanelRegistrationOpen = "open"
	// PanelRegistrationDisabled 完全关闭自助注册。
	PanelRegistrationDisabled = "disabled"
)

// Config 保存进程启动所需的全部配置项。
//
// 用户限额（RPM / success limit）不再可配置，统一由 tier 决定；
// DefaultUserRPM 仅作为内存限流器在 tier 解析异常时的兜底，不再用于新用户。
type Config struct {
	CPABaseURL            string
	CPAAPIKey             string
	Model                 string
	Timeout               time.Duration
	Debug                 bool
	HTTPAddr              string
	DBPath                string
	JWTSecret             string
	DefaultUserRPM        int
	PanelRegistrationMode string
	SetupToken            string
	MCPIPRPM              int
}

// Load 读取并校验配置。
func Load() (*Config, error) {
	cfg := &Config{
		CPABaseURL:            strings.TrimRight(envOrDefault("CPA_BASE_URL", defaultBaseURL), "/"),
		CPAAPIKey:             strings.TrimSpace(os.Getenv("CPA_API_KEY")),
		Model:                 envOrDefault("GROK_MODEL", defaultModel),
		Timeout:               defaultTimeout,
		Debug:                 parseBoolEnv("GROK_MCP_DEBUG"),
		HTTPAddr:              envOrDefault("GROK_HTTP_ADDR", defaultHTTPAddr),
		DBPath:                envOrDefault("GROK_DB_PATH", defaultDBPath),
		JWTSecret:             strings.TrimSpace(os.Getenv("GROK_JWT_SECRET")),
		DefaultUserRPM:        defaultLimiterRPM,
		PanelRegistrationMode: strings.ToLower(envOrDefault("GROK_PANEL_REGISTRATION", PanelRegistrationBootstrapOnly)),
		SetupToken:            strings.TrimSpace(os.Getenv("GROK_SETUP_TOKEN")),
		MCPIPRPM:              defaultMCPIPRPM,
	}

	if raw := strings.TrimSpace(os.Getenv("GROK_HTTP_TIMEOUT")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf("GROK_HTTP_TIMEOUT must be a positive integer (seconds), got %q", raw)
		}
		cfg.Timeout = time.Duration(seconds) * time.Second
	}

	// GROK_DEFAULT_USER_RPM 仅用于内存限流器的兜底；用户实际 RPM 始终取自 tier。
	if raw := strings.TrimSpace(os.Getenv("GROK_DEFAULT_USER_RPM")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("GROK_DEFAULT_USER_RPM must be a positive integer, got %q", raw)
		}
		cfg.DefaultUserRPM = n
	}

	if raw := strings.TrimSpace(os.Getenv("GROK_MCP_IP_RPM")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("GROK_MCP_IP_RPM must be a positive integer, got %q", raw)
		}
		cfg.MCPIPRPM = n
	}

	if cfg.CPAAPIKey == "" {
		return nil, fmt.Errorf("CPA_API_KEY is required")
	}
	if cfg.CPABaseURL == "" {
		return nil, fmt.Errorf("CPA_BASE_URL must not be empty")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("GROK_MODEL must not be empty")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("GROK_JWT_SECRET is required")
	}
	switch cfg.PanelRegistrationMode {
	case PanelRegistrationBootstrapOnly, PanelRegistrationOpen, PanelRegistrationDisabled:
	default:
		return nil, fmt.Errorf("GROK_PANEL_REGISTRATION must be one of bootstrap-only, open, or disabled, got %q", cfg.PanelRegistrationMode)
	}
	// HS256 的安全性依赖密钥长度；短密钥可被离线暴力破解伪造 token。
	// RFC 7518 推荐 HS256 使用至少 256 位（32 字节）密钥，此处据此拒绝弱密钥。
	const minJWTSecretLen = 32
	if len(cfg.JWTSecret) < minJWTSecretLen {
		return nil, fmt.Errorf("GROK_JWT_SECRET must be at least %d bytes to avoid weak-key attacks on HS256", minJWTSecretLen)
	}

	return cfg, nil
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
