package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/grok-mcp/internal/admin"
	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/ratelimit"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/usage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runHTTP 启动 HTTP 模式：/mcp 为 MCP 协议端点，/admin 为密钥与统计管理 API。
//
// MCP 请求中间件链（由外到内）：
//  1. auth.APIKeyMiddleware — Bearer API Key，校验后把密钥写入 context；
//  2. ratelimit — 按密钥的 rate_limit 做令牌桶限流；
//  3. usage.MCPMiddleware — 记录调用次数与工具名、耗时（异步写库）；
//  4. mcp.StreamableHTTPHandler — 实际 MCP 会话处理。
func runHTTP(ctx context.Context, cfg *config.Config, server *mcp.Server) error {
	st, err := store.OpenSQLite(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// 用量明细写入走后台队列，避免 MCP 热路径被 SQLite 阻塞。
	usageWriter := store.NewAsyncUsageWriter(st, 256)
	defer usageWriter.Close()

	lim := ratelimit.New(cfg.DefaultRateLimit)
	defer lim.Close()

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	var mcpChain http.Handler = mcpHandler
	mcpChain = usage.MCPMiddleware(st, usageWriter)(mcpChain)
	mcpChain = lim.Middleware()(mcpChain)
	mcpChain = auth.APIKeyMiddleware(st)(mcpChain)

	rootMux := http.NewServeMux()
	rootMux.Handle("/mcp/", mcpChain)
	rootMux.Handle("/mcp", mcpChain)

	adminMux := admin.NewMux(&admin.Handler{Store: st})
	adminHandler := auth.AdminTokenMiddleware(cfg.AdminToken)(adminMux)
	rootMux.Handle("/admin/", adminHandler)
	rootMux.Handle("/admin", adminHandler)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           rootMux,
		ReadHeaderTimeout: 10 * time.Second,
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
