// Package version 保存构建时可注入的发布版本号。
package version

// Version 为当前发布版本。构建时可通过 ldflags 覆盖，例如：
//
//	go build -ldflags "-X github.com/grok-mcp/internal/version.Version=1.2.3" ./cmd/grok-mcp
var Version = "0.1.0"