package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
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
)

// runHTTP 启动 HTTP 模式：/mcp 为 MCP 端点，/panel 为管理面板 API。
func runHTTP(ctx context.Context, cfg *config.Config, server *mcp.Server) error {
	st, err := store.OpenSQLite(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	usageWriter := store.NewAsyncUsageWriter(st, 256)
	defer usageWriter.Close()

	userLim := ratelimit.NewUserLimiter(cfg.DefaultUserRPM)
	defer userLim.Close()

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
		Handler:           rootMux,
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
