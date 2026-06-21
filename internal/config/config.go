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
	defaultBaseURL          = "http://127.0.0.1:8317"
	defaultModel            = "grok-4.3"
	defaultTimeout          = 120 * time.Second
	defaultTransport        = "stdio"
	defaultHTTPAddr         = ":8080"
	defaultDBPath           = "./grok-mcp.db"
	defaultDefaultRateLimit = 60
)

// Config 保存进程启动所需的全部配置项。
//
// 环境变量一览：
//   - CPA_BASE_URL — 上游 CPA/Grok 网关根地址（默认 http://127.0.0.1:8317）
//   - CPA_API_KEY — 调用上游的 Bearer 密钥（必填）
//   - GROK_MODEL — 默认模型名
//   - GROK_HTTP_TIMEOUT — 上游 HTTP 超时（秒）
//   - GROK_MCP_DEBUG — 为 1/true/yes 时输出调试日志
//   - GROK_TRANSPORT — stdio 或 http
//   - GROK_HTTP_ADDR — HTTP 监听地址（http 模式）
//   - GROK_DB_PATH — SQLite 路径（http 模式）
//   - GROK_ADMIN_TOKEN — 管理 API 的静态 Bearer（http 模式必填）
//   - GROK_DEFAULT_RATE_LIMIT — 新密钥未单独设置时的默认「次/分钟」
type Config struct {
	CPABaseURL        string
	CPAAPIKey         string
	Model             string
	Timeout           time.Duration
	Debug             bool
	Transport         string
	HTTPAddr          string
	DBPath            string
	AdminToken        string
	DefaultRateLimit  int
}

// Load 读取并校验配置；缺少 CPA_API_KEY 或 http 模式缺少 GROK_ADMIN_TOKEN 时会返回错误。
func Load() (*Config, error) {
	cfg := &Config{
		CPABaseURL:       strings.TrimRight(envOrDefault("CPA_BASE_URL", defaultBaseURL), "/"),
		CPAAPIKey:        strings.TrimSpace(os.Getenv("CPA_API_KEY")),
		Model:            envOrDefault("GROK_MODEL", defaultModel),
		Timeout:          defaultTimeout,
		Debug:            parseBoolEnv("GROK_MCP_DEBUG"),
		Transport:        strings.ToLower(envOrDefault("GROK_TRANSPORT", defaultTransport)),
		HTTPAddr:         envOrDefault("GROK_HTTP_ADDR", defaultHTTPAddr),
		DBPath:           envOrDefault("GROK_DB_PATH", defaultDBPath),
		AdminToken:       strings.TrimSpace(os.Getenv("GROK_ADMIN_TOKEN")),
		DefaultRateLimit: defaultDefaultRateLimit,
	}

	if raw := strings.TrimSpace(os.Getenv("GROK_HTTP_TIMEOUT")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf("GROK_HTTP_TIMEOUT must be a positive integer (seconds), got %q", raw)
		}
		cfg.Timeout = time.Duration(seconds) * time.Second
	}

	if raw := strings.TrimSpace(os.Getenv("GROK_DEFAULT_RATE_LIMIT")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("GROK_DEFAULT_RATE_LIMIT must be a positive integer, got %q", raw)
		}
		cfg.DefaultRateLimit = n
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

	if cfg.Transport != "stdio" && cfg.Transport != "http" {
		return nil, fmt.Errorf("GROK_TRANSPORT must be stdio or http, got %q", cfg.Transport)
	}
	if cfg.Transport == "http" && cfg.AdminToken == "" {
		return nil, fmt.Errorf("GROK_ADMIN_TOKEN is required when GROK_TRANSPORT=http")
	}

	return cfg, nil
}

// envOrDefault 在环境变量非空时返回其值，否则返回 fallback。
func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// parseBoolEnv 将常见真值字符串（1、true、yes）解析为 true。
func parseBoolEnv(key string) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	return raw == "1" || raw == "true" || raw == "yes"
}