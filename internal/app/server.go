// Package app is the HTTP composition root for grok-mcp.
//
// It wires store, auth, rate limits, quota/usage middleware, panel API, and the
// Streamable HTTP MCP endpoint. cmd/grok-mcp stays a thin entrypoint.
package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/grok-mcp/internal/auth"
	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/grok"
	"github.com/grok-mcp/internal/logx"
	mcpserver "github.com/grok-mcp/internal/mcp"
	"github.com/grok-mcp/internal/panel"
	"github.com/grok-mcp/internal/panelui"
	"github.com/grok-mcp/internal/quota"
	"github.com/grok-mcp/internal/ratelimit"
	"github.com/grok-mcp/internal/store"
	"github.com/grok-mcp/internal/usage"
	"github.com/grok-mcp/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/bcrypt"
)

const bootstrapAdminUsername = "admin"

var contentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self'",
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
	"font-src 'self' https://fonts.gstatic.com data:",
	"img-src 'self' data: blob: https:",
	"connect-src 'self'",
	"base-uri 'none'",
	"frame-ancestors 'none'",
	"form-action 'self'",
}, "; ")

// BootstrapAdminCredentials holds the one-time bootstrap admin password printed at startup.
type BootstrapAdminCredentials struct {
	Username string
	Password string
}

// HTTPDependencies contains the initialized resources needed to build the
// production HTTP routing tree. Resource ownership remains with the caller.
type HTTPDependencies struct {
	Store          store.Store
	MCPServer      *mcp.Server
	UsageWriter    *store.AsyncUsageWriter
	UserLimiter    *ratelimit.UserLimiter
	MCPIPLimiter   *ratelimit.IPLimiter
	APIKeyResolver auth.APIKeyResolver
	PanelHandler   *panel.Handler
}

// Run starts the HTTP server (MCP + panel) and blocks until ctx is cancelled or ListenAndServe fails.
func Run(ctx context.Context, cfg *config.Config) error {
	st, err := store.OpenSQLite(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	serverSettings, err := InitializeServerSettings(ctx, st, cfg)
	if err != nil {
		return fmt.Errorf("initialize server settings: %w", err)
	}

	debugState := logx.NewDebugState(serverSettings.Debug)
	grokClient, err := grok.NewClientWithServerSettings(serverSettings, debugState)
	if err != nil {
		return fmt.Errorf("create grok client: %w", err)
	}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "grok-mcp", Version: version.Version}, &mcp.ServerOptions{
		Instructions: mcpserver.ServerInstructions,
	})
	mcpserver.RegisterToolsWithLogger(mcpServer, grokClient, logx.NewWithDebugState("mcp", debugState))

	bootstrapCredentials, err := EnsureBootstrapAdmin(ctx, st)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if bootstrapCredentials != nil {
		log.Printf("BOOTSTRAP ADMIN CREATED username=%s password=%s", bootstrapCredentials.Username, bootstrapCredentials.Password)
	}

	usageWriter := store.NewAsyncUsageWriter(st, 256)
	defer usageWriter.Close()

	userLimiter := ratelimit.NewUserLimiter()
	defer userLimiter.Close()
	mcpIPLimiter := ratelimit.NewIPLimiter(cfg.MCPIPRPM)
	mcpIPLimiter.SetTrustedProxies(cfg.TrustedProxies)
	defer mcpIPLimiter.Close()

	authResolver := auth.NewCachedAPIKeyResolver(st, 30*time.Second)
	panelHandler := &panel.Handler{
		Store:                 st,
		JWTSecret:             cfg.JWTSecret,
		TrustedProxies:        cfg.TrustedProxies,
		InitialServerSettings: serverSettings,
		SettingsApplier:       grokClient,
		ModelLister:           grokClient,
		AuthCache:             authResolver,
	}
	httpHandler := BuildHTTPHandler(HTTPDependencies{
		Store:          st,
		MCPServer:      mcpServer,
		UsageWriter:    usageWriter,
		UserLimiter:    userLimiter,
		MCPIPLimiter:   mcpIPLimiter,
		APIKeyResolver: authResolver,
		PanelHandler:   panelHandler,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
		// MaxBytesReader only caps request size. ReadTimeout also bounds how long a
		// client may take to send the body after headers, mitigating slow-body DoS.
		ReadTimeout: 30 * time.Second,
		// Streaming MCP responses can outlive any startup timeout value, and the
		// upstream timeout is hot-reloadable. Keep the server write timeout off.
		WriteTimeout: 0,
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

// BuildHTTPHandler creates the single production routing and middleware tree
// shared by the real server and HTTP integration tests.
func BuildHTTPHandler(dependencies HTTPDependencies) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return dependencies.MCPServer
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	// MCP middleware is wrapped from inside out. The effective request order is:
	// MaxBody -> IP RPM -> API Key -> ExtractToolName -> User RPM -> Quota -> Usage -> MCP.
	var mcpChain http.Handler = mcpHandler
	mcpChain = usage.MCPMiddleware(dependencies.Store, dependencies.UsageWriter)(mcpChain)
	mcpChain = quota.MCPMiddleware(dependencies.Store)(mcpChain)
	mcpChain = dependencies.UserLimiter.UserMiddleware()(mcpChain)
	mcpChain = usage.ExtractToolNameMiddleware()(mcpChain)
	mcpChain = auth.APIKeyMiddleware(dependencies.APIKeyResolver)(mcpChain)
	mcpChain = dependencies.MCPIPLimiter.Middleware()(mcpChain)
	mcpChain = panel.MaxBodyMiddleware(panel.MaxPanelBodyBytes())(mcpChain)

	rootMux := http.NewServeMux()
	rootMux.Handle("/mcp/", mcpChain)
	rootMux.Handle("/mcp", mcpChain)

	panelChain := panel.MaxBodyMiddleware(panel.MaxPanelBodyBytes())(panel.NewMux(dependencies.PanelHandler))
	rootMux.Handle("/panel/v1/", panelChain)
	rootMux.Handle("/panel/v1", panelChain)

	panelUI := panelui.Handler()
	rootMux.Handle("/panel/", panelUI)
	rootMux.Handle("/panel", panelUI)
	return SecurityHeadersMiddleware(rootMux)
}

// InitializeServerSettings loads DB settings (or environment defaults),
// validates the effective settings, persists them, and returns the sole runtime view.
func InitializeServerSettings(ctx context.Context, st store.Store, cfg *config.Config) (config.ServerSettings, error) {
	storedSettings, err := st.GetServerSettings(ctx)
	if err != nil {
		return config.ServerSettings{}, fmt.Errorf("load settings: %w", err)
	}

	settings := cfg.ServerSettings()
	if storedSettings != nil {
		settings = config.ServerSettingsFromStore(storedSettings)
	}

	normalizedSettings, err := config.NormalizeServerSettings(settings)
	if err != nil {
		return config.ServerSettings{}, err
	}
	if _, err := st.UpsertServerSettings(ctx, config.StoreServerSettings(normalizedSettings)); err != nil {
		return config.ServerSettings{}, fmt.Errorf("persist settings: %w", err)
	}
	return normalizedSettings, nil
}

// EnsureBootstrapAdmin creates a default admin when no enabled admin exists.
func EnsureBootstrapAdmin(ctx context.Context, st store.Store) (*BootstrapAdminCredentials, error) {
	enabledAdminCount, err := st.CountEnabledAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("count enabled admins: %w", err)
	}
	if enabledAdminCount > 0 {
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
		if errors.Is(err, store.ErrUsernameTaken) {
			return promoteExistingBootstrapAdmin(ctx, st, string(passwordHash), password)
		}
		return nil, fmt.Errorf("create admin user: %w", err)
	}

	return &BootstrapAdminCredentials{Username: bootstrapAdminUsername, Password: password}, nil
}

// promoteExistingBootstrapAdmin 在 "admin" 用户名已被普通用户占用时，
// 将其提升为启用状态的管理员并重置密码，返回新的凭证。
func promoteExistingBootstrapAdmin(ctx context.Context, st store.Store, passwordHash, password string) (*BootstrapAdminCredentials, error) {
	existingUser, err := st.GetUserByUsername(ctx, bootstrapAdminUsername)
	if err != nil {
		return nil, fmt.Errorf("lookup existing admin user: %w", err)
	}
	if existingUser == nil {
		return nil, fmt.Errorf("username taken but user not found")
	}
	enabled := true
	adminRole := store.RoleAdmin
	revokeTokens := true
	if _, err := st.UpdateUser(ctx, existingUser.ID, store.UserUpdates{
		Enabled:      &enabled,
		Role:         &adminRole,
		PasswordHash: &passwordHash,
		RevokeTokens: &revokeTokens,
	}); err != nil {
		return nil, fmt.Errorf("promote existing admin: %w", err)
	}
	return &BootstrapAdminCredentials{Username: bootstrapAdminUsername, Password: password}, nil
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

// SecurityHeadersMiddleware attaches baseline browser security headers.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
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
