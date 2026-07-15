package config

import "github.com/grok-mcp/internal/store"

// ServerSettingsFromFields builds runtime settings from the shared field set.
func ServerSettingsFromFields(fields store.SettingsFields) ServerSettings {
	return ServerSettings{
		CPABaseURL:                 fields.CPABaseURL,
		CPAAPIKey:                  fields.CPAAPIKey,
		UpstreamProtocol:           UpstreamProtocol(fields.UpstreamProtocol),
		Model:                      fields.Model,
		TimeoutSeconds:             fields.TimeoutSeconds,
		MCPGlobalSearchConcurrency: fields.MCPGlobalSearchConcurrency,
		MCPUserSearchConcurrency:   fields.MCPUserSearchConcurrency,
		ProxyURL:                   fields.ProxyURL,
		ProxyEnabled:               fields.ProxyEnabled,
		RegistrationMode:           fields.RegistrationMode,
		Debug:                      fields.Debug,
	}
}

// SettingsFieldsFromConfig extracts the shared field set from runtime settings.
func SettingsFieldsFromConfig(settings ServerSettings) store.SettingsFields {
	return store.SettingsFields{
		CPABaseURL:                 settings.CPABaseURL,
		CPAAPIKey:                  settings.CPAAPIKey,
		UpstreamProtocol:           string(settings.UpstreamProtocol),
		Model:                      settings.Model,
		TimeoutSeconds:             settings.TimeoutSeconds,
		MCPGlobalSearchConcurrency: settings.MCPGlobalSearchConcurrency,
		MCPUserSearchConcurrency:   settings.MCPUserSearchConcurrency,
		ProxyURL:                   settings.ProxyURL,
		ProxyEnabled:               settings.ProxyEnabled,
		RegistrationMode:           settings.RegistrationMode,
		Debug:                      settings.Debug,
	}
}
