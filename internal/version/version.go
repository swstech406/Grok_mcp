// Package version 提供 grok-search-mcp 的构建版本信息。
//
// 源码中的版本号是本地构建和 Docker 构建的统一版本来源，确保命令行、
// MCP 元数据和管理面板显示同一版本。
package version

// Version 是当前构建的语义化版本号。
//
// 默认值用于未注入版本号的本地构建。发布构建可通过 ldflags 覆盖，例如：
//
//	go build -ldflags "-X github.com/MapleMapleCat/Grok_Search_Mcp/internal/version.Version=1.2.3" ./cmd/grok-search-mcp
//
// 注入值会原样展示，项目约定使用不带 "v" 前缀的版本号。
var Version = "0.2.0"
