// grok-mcp 是 Grok 搜索能力的 MCP（Model Context Protocol）服务端。
//
// 支持两种传输方式：
//   - stdio：供 Cursor、Claude Desktop 等本地 MCP 客户端通过标准输入输出连接；
//   - http：对外提供 Streamable HTTP MCP 端点，并附带 API Key 鉴权、限流与用量统计。
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
	server := mcp.NewServer(&mcp.Implementation{Name: "grok-mcp", Version: version.Version}, nil)
	mcpserver.RegisterTools(server, client, cfg.Debug)

	// 优雅退出：SIGINT / SIGTERM 时取消 context，HTTP 模式会触发 Shutdown。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Transport == "http" {
		if err := runHTTP(ctx, cfg, server); err != nil {
			log.Fatalf("server error: %v", err)
		}
		return
	}

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server error: %v", err)
	}
}