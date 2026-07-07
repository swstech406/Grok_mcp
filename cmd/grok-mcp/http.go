package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/panel"
	"github.com/grok-mcp/internal/panelui"
	"github.com/grok-mcp/internal/quota"
	"github.com/grok-mcp/internal/ratelimit"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/usage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/bcrypt"
)

const bootstrapAdminUsername = "admin"

var contentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self'",
	"style-src 'self' https://fonts.googleapis.com",
	"font-src 'self' https://fonts.gstatic.com data:",
	"img-src 'self' data: blob: https:",
	"connect-src 'self'",
	"base-uri 'none'",
	"frame-ancestors 'none'",
	"form-action 'self'",
}, "; ")

type bootstrapAdminCredentials struct {
	Username string
	Password string
}

// runHTTP 启动 HTTP 模式：/mcp 为 MCP 端点，/panel 为管理面板 API。
func runHTTP(ctx context.Context, cfg *config.Config, server *mcp.Server) error {
	st, err := store.OpenSQLite(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	bootstrapCredentials, err := ensureBootstrapAdmin(ctx, st)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if bootstrapCredentials != nil {
		log.Printf("BOOTSTRAP ADMIN CREATED username=%s password=%s", bootstrapCredentials.Username, bootstrapCredentials.Password)
	}

	usageWriter := store.NewAsyncUsageWriter(st, 256)
	defer usageWriter.Close()

	userLim := ratelimit.NewUserLimiter(cfg.DefaultUserRPM)
	defer userLim.Close()
	mcpIPLimiter := ratelimit.NewIPLimiter(cfg.MCPIPRPM)
	defer mcpIPLimiter.Close()

	authResolver := auth.NewCachedAPIKeyResolver(st, 30*time.Second)

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	var mcpChain http.Handler = mcpHandler
	mcpChain = panel.MaxBodyMiddleware(panel.MaxPanelBodyBytes())(mcpChain)
	mcpChain = usage.MCPMiddleware(st, usageWriter)(mcpChain)
	mcpChain = quota.MCPMiddleware(st)(mcpChain)
	mcpChain = usage.ExtractToolNameMiddleware()(mcpChain)
	mcpChain = userLim.UserMiddleware()(mcpChain)
	mcpChain = auth.APIKeyMiddleware(authResolver)(mcpChain)
	mcpChain = mcpIPLimiter.Middleware()(mcpChain)

	rootMux := http.NewServeMux()
	rootMux.Handle("/mcp/", mcpChain)
	rootMux.Handle("/mcp", mcpChain)

	panelHandler := &panel.Handler{Store: st, Config: cfg, AuthCache: authResolver}
	panelMux := panel.NewMux(panelHandler)
	jwtSkip := map[string]struct{}{
		"/panel/v1/auth/register": {},
		"/panel/v1/auth/login":    {},
	}
	var panelChain http.Handler = panelMux
	panelChain = panel.MaxBodyMiddleware(panel.MaxPanelBodyBytes())(panelChain)
	panelChain = auth.JWTMiddleware(cfg.JWTSecret, st, jwtSkip)(panelChain)
	rootMux.Handle("/panel/v1/", panelChain)
	rootMux.Handle("/panel/v1", panelChain)

	panelUI := panelui.Handler()
	rootMux.Handle("/panel/", panelUI)
	rootMux.Handle("/panel", panelUI)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           securityHeadersMiddleware(rootMux),
		ReadHeaderTimeout: 10 * time.Second,
		// MaxBytesReader only caps request size. ReadTimeout also bounds how long a
		// client may take to send the body after headers, mitigating slow-body DoS.
		ReadTimeout: 30 * time.Second,
		// SSE 流式响应（/mcp tools/call）是长连接，WriteTimeout 不能短于上游超时；
		// 设为略大于 cfg.Timeout 兜底，避免在合法的长时间搜索中被中断。
		WriteTimeout: cfg.Timeout + 30*time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if isWildcardHTTPAddr(cfg.HTTPAddr) {
			log.Printf("WARNING: grok-mcp is listening on %s without built-in TLS; use an HTTPS reverse proxy before exposing it publicly", cfg.HTTPAddr)
		}
		log.Printf("grok-mcp HTTP listening on %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func ensureBootstrapAdmin(ctx context.Context, st store.Store) (*bootstrapAdminCredentials, error) {
	userCount, err := st.CountUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("count users: %w", err)
	}
	if userCount > 0 {
		return nil, nil
	}

	password, err := randomBootstrapPassword(12)
	if err != nil {
		return nil, fmt.Errorf("generate password: %w", err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	if _, err := st.CreateUser(ctx, bootstrapAdminUsername, string(passwordHash), store.RoleAdmin); err != nil {
		return nil, fmt.Errorf("create admin user: %w", err)
	}

	return &bootstrapAdminCredentials{Username: bootstrapAdminUsername, Password: password}, nil
}

func randomBootstrapPassword(length int) (string, error) {
	const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	if length <= 0 {
		return "", fmt.Errorf("password length must be positive")
	}

	passwordBytes := make([]byte, length)
	maxIndex := big.NewInt(int64(len(passwordAlphabet)))
	for index := range passwordBytes {
		randomIndex, err := rand.Int(rand.Reader, maxIndex)
		if err != nil {
			return "", err
		}
		passwordBytes[index] = passwordAlphabet[randomIndex.Int64()]
	}
	return string(passwordBytes), nil
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		if isSensitiveHTTPPath(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func isSensitiveHTTPPath(requestPath string) bool {
	return requestPath == "/mcp" || strings.HasPrefix(requestPath, "/mcp/") || requestPath == "/panel/v1" || strings.HasPrefix(requestPath, "/panel/v1/")
}

func isWildcardHTTPAddr(httpAddr string) bool {
	trimmedAddr := strings.TrimSpace(httpAddr)
	if trimmedAddr == "" || strings.HasPrefix(trimmedAddr, ":") {
		return true
	}
	host, _, err := net.SplitHostPort(trimmedAddr)
	if err != nil {
		return false
	}
	return host == "" || host == "0.0.0.0" || host == "::" || host == "[::]"
}
