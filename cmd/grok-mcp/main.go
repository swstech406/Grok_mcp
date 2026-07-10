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

	"github.com/grok-mcp/internal/app"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/version"
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

	// 优雅退出：SIGINT / SIGTERM 时取消 context，触发 HTTP Server Shutdown。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
