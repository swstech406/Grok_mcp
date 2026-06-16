package version

// Version is the release version. Override at build time with:
// go build -ldflags "-X github.com/grok-mcp/internal/version.Version=1.2.3" ./cmd/grok-mcp
var Version = "0.1.0"