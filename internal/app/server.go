// Package app is the HTTP composition root for grok-search-mcp.
//
// It wires store, auth, rate limits, quota/usage middleware, panel API, and the
// Streamable HTTP MCP endpoint. cmd/grok-search-mcp stays a thin entrypoint.
package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/auth"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/grok"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/logx"
	mcpserver "github.com/MapleMapleCat/Grok_Search_Mcp/internal/mcp"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/panel"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/panelui"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/quota"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/ratelimit"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/usage"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const bootstrapAdminUsername = "admin"

var contentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self'",
	"worker-src 'self'",
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
	"font-src 'self' https://fonts.gstatic.com data:",
	"img-src 'self' data: blob: https:",
	"connect-src 'self'",
	"base-uri 'none'",
	"frame-ancestors 'none'",
	"form-action 'self'",
}, "; ")

// HTTPDependencies contains the initialized resources needed to build the
// production HTTP routing tree. Resource ownership remains with the caller.
type HTTPDependencies struct {
	Store                    store.Store
	MCPServer                *mcp.Server
	UsageWriter              *store.AsyncUsageWriter
	UserLimiter              *ratelimit.UserLimiter
	SearchConcurrencyLimiter *ratelimit.SearchConcurrencyLimiter
	MCPIPLimiter             *ratelimit.IPLimiter
	APIKeyResolver           auth.APIKeyResolver
	PanelHandler             *panel.Handler
	DebugState               *logx.DebugState
}

type upstreamServerSettingsApplier interface {
	ApplyServerSettings(config.ServerSettings) error
}

type runtimeServerSettingsApplier struct {
	upstreamApplier          upstreamServerSettingsApplier
	searchConcurrencyLimiter *ratelimit.SearchConcurrencyLimiter
	sqliteStore              *store.SQLiteStore
	usageWriter              *store.AsyncUsageWriter
	liveVersion              atomic.Int64
}

func (applier *runtimeServerSettingsApplier) ApplyServerSettings(settings config.ServerSettings, persistedVersion int64) error {
	if applier.sqliteStore != nil {
		applier.sqliteStore.SetMetricsEnabled(settings.OperationsMetricsEnabled)
	}
	if applier.usageWriter != nil {
		applier.usageWriter.SetMetricsEnabled(settings.OperationsMetricsEnabled)
	}
	if applier.upstreamApplier != nil {
		if err := applier.upstreamApplier.ApplyServerSettings(settings); err != nil {
			return err
		}
	}
	if applier.searchConcurrencyLimiter != nil {
		if err := applier.searchConcurrencyLimiter.UpdateLimits(
			settings.MCPGlobalSearchConcurrency,
			settings.MCPUserSearchConcurrency,
		); err != nil {
			return err
		}
	}
	applier.liveVersion.Store(persistedVersion)
	return nil
}

func (applier *runtimeServerSettingsApplier) LiveServerSettingsVersion() int64 {
	return applier.liveVersion.Load()
}

// Run starts the HTTP server (MCP + panel) and blocks until ctx is cancelled or ListenAndServe fails.
func Run(ctx context.Context, cfg *config.Config) error {
	st, err := store.OpenSQLite(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := st.ConfigureAPIKeyEncryption(cfg.JWTSecret); err != nil {
		return fmt.Errorf("configure API key encryption: %w", err)
	}

	storedServerSettings, err := InitializeServerSettings(ctx, st, cfg)
	if err != nil {
		return fmt.Errorf("initialize server settings: %w", err)
	}
	serverSettings := storedServerSettings.Runtime
	st.SetMetricsEnabled(serverSettings.OperationsMetricsEnabled)

	debugState := logx.NewDebugState(serverSettings.Debug)
	grokClient, err := grok.NewClientWithServerSettings(serverSettings, debugState)
	if err != nil {
		return fmt.Errorf("create grok client: %w", err)
	}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "grok-search-mcp", Version: version.Version}, &mcp.ServerOptions{
		Instructions: mcpserver.ServerInstructions,
	})
	mcpserver.RegisterToolsWithLogger(mcpServer, grokClient, logx.NewWithDebugState("mcp", debugState))

	bootstrapCredentials, err := EnsureBootstrapAdmin(ctx, st, cfg.BootstrapCredentialsPath)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if bootstrapCredentials != nil {
		logBootstrapCredentialsAvailable(cfg.BootstrapCredentialsPath)
	}

	usageWriter := store.NewAsyncUsageWriter(st, 256)
	usageWriter.SetMetricsEnabled(serverSettings.OperationsMetricsEnabled)
	defer usageWriter.Close()
	usageMaintenanceRunner, err := store.StartUsageMaintenance(
		ctx,
		st,
		store.UsageRetentionPolicy{
			RawRetention:    cfg.UsageRawRetention,
			HourlyRetention: cfg.UsageHourlyRetention,
			DailyRetention:  cfg.UsageDailyRetention,
		},
		cfg.UsageMaintenanceInterval,
	)
	if err != nil {
		return fmt.Errorf("start usage maintenance: %w", err)
	}
	defer usageMaintenanceRunner.Close()

	userLimiter := ratelimit.NewUserLimiterWithConfig(ratelimit.UserLimiterConfig{
		MaximumEntries:      cfg.UserRPMMaximumEntries,
		FallbackBucketCount: cfg.UserRPMFallbackBuckets,
	})
	defer userLimiter.Close()
	searchConcurrencyLimiter := ratelimit.NewSearchConcurrencyLimiter(
		serverSettings.MCPGlobalSearchConcurrency,
		serverSettings.MCPUserSearchConcurrency,
	)
	defer searchConcurrencyLimiter.Close()
	clientIPResolver := ratelimit.NewClientIPResolverWithConfig(ratelimit.ClientIPResolverConfig{
		Mode:                 cfg.ClientIPMode,
		TrustedProxyPrefixes: cfg.TrustedProxyCIDRs,
	})
	mcpIPLimiter := ratelimit.NewIPLimiterWithConfig(ratelimit.IPLimiterConfig{
		RequestsPerMinute:       cfg.MCPIPRPM,
		ClientIPResolver:        clientIPResolver,
		MaximumEntriesPerShard:  cfg.MCPIPMaxEntriesPerShard,
		FallbackBucketsPerShard: cfg.MCPIPFallbackBucketsPerShard,
	})
	defer mcpIPLimiter.Close()

	authResolver := auth.NewCachedAPIKeyResolverWithConfig(st, auth.APIKeyCacheConfig{
		TTL:               30 * time.Second,
		MissMaxConcurrent: cfg.AuthKeyMissMaxConcurrent,
	})
	defer authResolver.Close()
	settingsApplier := &runtimeServerSettingsApplier{
		upstreamApplier:          grokClient,
		searchConcurrencyLimiter: searchConcurrencyLimiter,
		sqliteStore:              st,
		usageWriter:              usageWriter,
	}
	settingsApplier.liveVersion.Store(storedServerSettings.Revision)
	panelHandler := &panel.Handler{
		Store:                    st,
		JWTSecret:                cfg.JWTSecret,
		MaxAPIKeysPerUser:        cfg.MaxAPIKeysPerUser,
		BootstrapAdminUsername:   bootstrapAdminUsername,
		BootstrapCredentialsPath: cfg.BootstrapCredentialsPath,
		BootstrapCredentialCleaner: func() error {
			return os.Remove(cfg.BootstrapCredentialsPath)
		},
		InitialServerSettings: serverSettings,
		SettingsApplier:       settingsApplier,
		ModelLister:           grokClient,
		AuthCache:             authResolver,
		AuthProtector: panel.NewAuthProtector(panel.AuthProtectorConfig{
			ClientIPResolver:          clientIPResolver,
			PasswordMaximumConcurrent: cfg.AuthPasswordMaxConcurrent,
		}),
		SQLiteMetrics:      st,
		UsageWriterMetrics: usageWriter,
		IPLimiterMetrics:   mcpIPLimiter,
		UserLimiterMetrics: userLimiter,
	}
	httpHandler := BuildHTTPHandler(HTTPDependencies{
		Store:                    st,
		MCPServer:                mcpServer,
		UsageWriter:              usageWriter,
		UserLimiter:              userLimiter,
		SearchConcurrencyLimiter: searchConcurrencyLimiter,
		MCPIPLimiter:             mcpIPLimiter,
		APIKeyResolver:           authResolver,
		PanelHandler:             panelHandler,
		DebugState:               debugState,
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
			log.Printf("WARNING: grok-search-mcp is listening on %s without built-in TLS; use an HTTPS reverse proxy before exposing it publicly", cfg.HTTPAddr)
		}
		log.Printf("grok-search-mcp HTTP listening on %s", cfg.HTTPAddr)
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
	// MaxBody -> IP RPM -> API Key -> ExtractToolName -> User RPM ->
	// Search Concurrency -> Quota -> Usage -> MCP.
	var mcpChain http.Handler = mcpHandler
	mcpChain = usage.MCPMiddleware(dependencies.Store, dependencies.UsageWriter, dependencies.DebugState)(mcpChain)
	mcpChain = quota.MCPMiddleware(dependencies.Store)(mcpChain)
	mcpChain = dependencies.SearchConcurrencyLimiter.Middleware(mcpserver.IsSearchToolName)(mcpChain)
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
func InitializeServerSettings(ctx context.Context, st store.Store, cfg *config.Config) (*store.ServerSettings, error) {
	storedSettings, err := st.GetServerSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	settings := cfg.ServerSettings()
	if storedSettings != nil {
		settings = storedSettings.Runtime
		// Databases created before search concurrency became panel-editable contain
		// migration sentinel values. Preserve their environment-derived limits on
		// the first upgraded start, then persist the effective positive values.
		if settings.MCPGlobalSearchConcurrency <= 0 {
			settings.MCPGlobalSearchConcurrency = cfg.MCPGlobalSearchConcurrency
		}
		if settings.MCPUserSearchConcurrency <= 0 {
			settings.MCPUserSearchConcurrency = cfg.MCPUserSearchConcurrency
		}
	}

	normalizedSettings, err := config.NormalizeServerSettings(settings)
	if err != nil {
		return nil, err
	}
	persistedSettings, err := st.UpsertServerSettings(ctx, store.ServerSettings{Runtime: normalizedSettings})
	if err != nil {
		return nil, fmt.Errorf("persist settings: %w", err)
	}
	return persistedSettings, nil
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
