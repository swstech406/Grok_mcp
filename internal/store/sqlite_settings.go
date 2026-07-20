package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const serverSettingsID = "default"

type storedServerSettingsAPIKey struct {
	ciphertext        string
	nonce             string
	encryptionVersion int
}

func scanServerSettings(row interface {
	Scan(dest ...any) error
}) (*ServerSettings, storedServerSettingsAPIKey, error) {
	var settings ServerSettings
	var storedAPIKey storedServerSettingsAPIKey
	var proxyEnabled int
	var registrationMode string
	var debug int
	var operationsMetricsEnabled int
	var createdAt string
	var updatedAt string
	err := row.Scan(
		&settings.ID,
		&settings.Revision,
		&settings.CPABaseURL,
		&storedAPIKey.ciphertext,
		&storedAPIKey.nonce,
		&storedAPIKey.encryptionVersion,
		&settings.UpstreamProtocol,
		&settings.Model,
		&settings.TimeoutSeconds,
		&settings.MCPGlobalSearchConcurrency,
		&settings.MCPUserSearchConcurrency,
		&settings.ProxyURL,
		&proxyEnabled,
		&registrationMode,
		&debug,
		&operationsMetricsEnabled,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, storedServerSettingsAPIKey{}, err
	}
	settings.ProxyEnabled = proxyEnabled != 0
	var normalizeErr error
	settings.RegistrationMode, normalizeErr = NormalizeRegistrationMode(RegistrationMode(registrationMode))
	if normalizeErr != nil {
		return nil, storedServerSettingsAPIKey{}, normalizeErr
	}
	settings.Debug = debug != 0
	settings.OperationsMetricsEnabled = operationsMetricsEnabled != 0
	var parseErr error
	settings.CreatedAt, parseErr = parseTime(createdAt)
	if parseErr != nil {
		return nil, storedServerSettingsAPIKey{}, parseErr
	}
	settings.UpdatedAt, parseErr = parseTime(updatedAt)
	if parseErr != nil {
		return nil, storedServerSettingsAPIKey{}, parseErr
	}
	return &settings, storedAPIKey, nil
}

const serverSettingsColumns = `id, revision, cpa_base_url, cpa_api_key_ciphertext, cpa_api_key_nonce, cpa_api_key_encryption_version, upstream_protocol, model, timeout_seconds, mcp_global_search_concurrency, mcp_user_search_concurrency, proxy_url, proxy_enabled, registration_mode, debug, operations_metrics_enabled, created_at, updated_at`

func serverSettingsAPIKeyRecordIdentity(settingsID string) string {
	return "server-settings:" + settingsID + ":cpa-api-key"
}

func (s *SQLiteStore) GetServerSettings(ctx context.Context) (*ServerSettings, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+serverSettingsColumns+` FROM server_settings WHERE id = ?`, serverSettingsID)
	return s.scanAndDecryptServerSettings(row)
}

func (s *SQLiteStore) scanAndDecryptServerSettings(row interface {
	Scan(dest ...any) error
}) (*ServerSettings, error) {
	settings, storedAPIKey, err := scanServerSettings(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	plaintextAPIKey, err := s.decryptServerSettingsAPIKey(settings.ID, storedAPIKey)
	if err != nil {
		return nil, err
	}
	settings.CPAAPIKey = plaintextAPIKey
	return settings, nil
}

func (s *SQLiteStore) decryptServerSettingsAPIKey(settingsID string, storedAPIKey storedServerSettingsAPIKey) (string, error) {
	hasCompleteCiphertext := storedAPIKey.ciphertext != "" && storedAPIKey.nonce != "" && storedAPIKey.encryptionVersion != 0
	if !hasCompleteCiphertext {
		return "", fmt.Errorf("server settings CPA API key ciphertext is incomplete")
	}
	plaintextAPIKey, err := s.secretCipher.Decrypt(
		storedAPIKey.ciphertext,
		storedAPIKey.nonce,
		serverSettingsAPIKeyRecordIdentity(settingsID),
		storedAPIKey.encryptionVersion,
	)
	if err != nil {
		return "", fmt.Errorf("decrypt server settings CPA API key: %w", err)
	}
	return plaintextAPIKey, nil
}

func (s *SQLiteStore) UpsertServerSettings(ctx context.Context, settings ServerSettings) (*ServerSettings, error) {
	cpaBaseURL := strings.TrimSpace(settings.CPABaseURL)
	cpaAPIKey := strings.TrimSpace(settings.CPAAPIKey)
	upstreamProtocol := strings.TrimSpace(string(settings.UpstreamProtocol))
	model := strings.TrimSpace(settings.Model)
	proxyURL := strings.TrimSpace(settings.ProxyURL)
	if cpaBaseURL == "" {
		return nil, fmt.Errorf("cpa_base_url is required")
	}
	if cpaAPIKey == "" {
		return nil, fmt.Errorf("cpa_api_key is required")
	}
	if upstreamProtocol == "" {
		return nil, fmt.Errorf("upstream_protocol is required")
	}
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if settings.TimeoutSeconds <= 0 {
		return nil, fmt.Errorf("timeout_seconds must be positive")
	}
	if settings.MCPGlobalSearchConcurrency <= 0 {
		return nil, fmt.Errorf("mcp_global_search_concurrency must be positive")
	}
	if settings.MCPUserSearchConcurrency <= 0 {
		return nil, fmt.Errorf("mcp_user_search_concurrency must be positive")
	}
	if settings.MCPUserSearchConcurrency > settings.MCPGlobalSearchConcurrency {
		return nil, fmt.Errorf("mcp_user_search_concurrency must not exceed mcp_global_search_concurrency")
	}
	registrationMode, err := NormalizeRegistrationMode(settings.RegistrationMode)
	if err != nil {
		return nil, err
	}
	ciphertext, nonce, encryptionVersion, err := s.secretCipher.Encrypt(
		cpaAPIKey,
		serverSettingsAPIKeyRecordIdentity(serverSettingsID),
	)
	if err != nil {
		return nil, fmt.Errorf("encrypt server settings CPA API key: %w", err)
	}

	proxyEnabled := 0
	if settings.ProxyEnabled {
		proxyEnabled = 1
	}
	debug := 0
	if settings.Debug {
		debug = 1
	}
	operationsMetricsEnabled := 0
	if settings.OperationsMetricsEnabled {
		operationsMetricsEnabled = 1
	}
	now := formatTime(time.Now().UTC())
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO server_settings (
			id, revision, cpa_base_url, cpa_api_key_ciphertext, cpa_api_key_nonce, cpa_api_key_encryption_version,
			upstream_protocol, model, timeout_seconds, mcp_global_search_concurrency, mcp_user_search_concurrency,
			proxy_url, proxy_enabled, registration_mode, debug, operations_metrics_enabled, created_at, updated_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			revision = server_settings.revision + 1,
			cpa_base_url = excluded.cpa_base_url,
			cpa_api_key_ciphertext = excluded.cpa_api_key_ciphertext,
			cpa_api_key_nonce = excluded.cpa_api_key_nonce,
			cpa_api_key_encryption_version = excluded.cpa_api_key_encryption_version,
			upstream_protocol = excluded.upstream_protocol,
			model = excluded.model,
			timeout_seconds = excluded.timeout_seconds,
			mcp_global_search_concurrency = excluded.mcp_global_search_concurrency,
			mcp_user_search_concurrency = excluded.mcp_user_search_concurrency,
			proxy_url = excluded.proxy_url,
			proxy_enabled = excluded.proxy_enabled,
			registration_mode = excluded.registration_mode,
			debug = excluded.debug,
			operations_metrics_enabled = excluded.operations_metrics_enabled,
			updated_at = excluded.updated_at
		RETURNING `+serverSettingsColumns,
		serverSettingsID,
		cpaBaseURL,
		ciphertext,
		nonce,
		encryptionVersion,
		upstreamProtocol,
		model,
		settings.TimeoutSeconds,
		settings.MCPGlobalSearchConcurrency,
		settings.MCPUserSearchConcurrency,
		proxyURL,
		proxyEnabled,
		string(registrationMode),
		debug,
		operationsMetricsEnabled,
		now,
		now,
	)
	storedSettings, err := s.scanAndDecryptServerSettings(row)
	if err != nil {
		return nil, fmt.Errorf("upsert server settings: %w", err)
	}
	return storedSettings, nil
}
