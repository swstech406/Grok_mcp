// grok-mcp 是 Grok 搜索能力的 MCP（Model Context Protocol）服务端。
//
// 通过 Streamable HTTP MCP 端点对外提供服务，并附带 API Key 鉴权、限流与用量统计。
//
// 上游通过 CPA 兼容的 /v1/responses 接口调用 Grok（web_search / x_search 工具）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/grok"
	mcpserver "github.com/grok-mcp/internal/mcp"
	"github.com/grok-mcp/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		os.Exit(0)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	client := grok.NewClient(cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "grok-mcp", Version: version.Version}, &mcp.ServerOptions{
		Instructions: mcpserver.ServerInstructions,
	})
	mcpserver.RegisterTools(server, client, cfg.Debug)

	// 优雅退出：SIGINT / SIGTERM 时取消 context，触发 HTTP Server Shutdown。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runHTTP(ctx, cfg, server); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
