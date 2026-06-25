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
	defaultBaseURL              = "http://127.0.0.1:8317"
	defaultModel                = "grok-4.3"
	defaultTimeout              = 120 * time.Second
	defaultHTTPAddr             = ":8080"
	defaultDBPath               = "./grok-mcp.db"
	defaultDefaultUserRPM       = 60
	defaultDefaultUserTotal     = 0
	defaultDefaultUserSuccess   = 0
)

// Config 保存进程启动所需的全部配置项。
type Config struct {
	CPABaseURL              string
	CPAAPIKey               string
	Model                   string
	Timeout                 time.Duration
	Debug                   bool
	HTTPAddr                string
	DBPath                  string
	JWTSecret               string
	DefaultUserRPM          int
	DefaultUserTotalLimit   int
	DefaultUserSuccessLimit int
}

// Load 读取并校验配置。
func Load() (*Config, error) {
	cfg := &Config{
		CPABaseURL:              strings.TrimRight(envOrDefault("CPA_BASE_URL", defaultBaseURL), "/"),
		CPAAPIKey:               strings.TrimSpace(os.Getenv("CPA_API_KEY")),
		Model:                   envOrDefault("GROK_MODEL", defaultModel),
		Timeout:                 defaultTimeout,
		Debug:                   parseBoolEnv("GROK_MCP_DEBUG"),
		HTTPAddr:                envOrDefault("GROK_HTTP_ADDR", defaultHTTPAddr),
		DBPath:                  envOrDefault("GROK_DB_PATH", defaultDBPath),
		JWTSecret:               strings.TrimSpace(os.Getenv("GROK_JWT_SECRET")),
		DefaultUserRPM:          defaultDefaultUserRPM,
		DefaultUserTotalLimit:   defaultDefaultUserTotal,
		DefaultUserSuccessLimit: defaultDefaultUserSuccess,
	}

	if raw := strings.TrimSpace(os.Getenv("GROK_HTTP_TIMEOUT")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return nil, fmt.Errorf("GROK_HTTP_TIMEOUT must be a positive integer (seconds), got %q", raw)
		}
		cfg.Timeout = time.Duration(seconds) * time.Second
	}

	if raw := strings.TrimSpace(os.Getenv("GROK_DEFAULT_USER_RPM")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("GROK_DEFAULT_USER_RPM must be a positive integer, got %q", raw)
		}
		cfg.DefaultUserRPM = n
	}
	if raw := strings.TrimSpace(os.Getenv("GROK_DEFAULT_USER_TOTAL_LIMIT")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("GROK_DEFAULT_USER_TOTAL_LIMIT must be a non-negative integer, got %q", raw)
		}
		cfg.DefaultUserTotalLimit = n
	}
	if raw := strings.TrimSpace(os.Getenv("GROK_DEFAULT_USER_SUCCESS_LIMIT")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("GROK_DEFAULT_USER_SUCCESS_LIMIT must be a non-negative integer, got %q", raw)
		}
		cfg.DefaultUserSuccessLimit = n
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