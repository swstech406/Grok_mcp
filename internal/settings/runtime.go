// Package settings defines the runtime-tunable server settings shared by
// configuration loading, persistence, the runtime client, and the panel.
package settings

import "fmt"

// UpstreamProtocol identifies the CPA-compatible HTTP protocol used for
// search requests.
type UpstreamProtocol string

const (
	UpstreamProtocolResponses         UpstreamProtocol = "responses"
	UpstreamProtocolChatCompletions   UpstreamProtocol = "chat_completions"
	UpstreamProtocolAnthropicMessages UpstreamProtocol = "anthropic_messages"
)

// RegistrationMode controls how the public registration endpoint accepts new
// users.
type RegistrationMode string

const (
	RegistrationModeFree     RegistrationMode = "free"
	RegistrationModeInvite   RegistrationMode = "invite"
	RegistrationModeDisabled RegistrationMode = "disabled"
)

// NormalizeRegistrationMode validates a persisted or configured registration
// mode without coupling the settings value object to the store package.
func NormalizeRegistrationMode(mode RegistrationMode) (RegistrationMode, error) {
	switch mode {
	case RegistrationModeFree:
		return RegistrationModeFree, nil
	case RegistrationModeInvite:
		return RegistrationModeInvite, nil
	case RegistrationModeDisabled:
		return RegistrationModeDisabled, nil
	default:
		return "", fmt.Errorf(
			"registration_mode must be one of %q, %q, or %q",
			RegistrationModeFree,
			RegistrationModeInvite,
			RegistrationModeDisabled,
		)
	}
}

// Runtime is the canonical value object for settings that can be changed at
// runtime. CPAAPIKey is deliberately excluded from JSON so this internal value
// cannot accidentally expose the upstream credential through a panel response.
type Runtime struct {
	CPABaseURL                 string
	CPAAPIKey                  string `json:"-"`
	UpstreamProtocol           UpstreamProtocol
	Model                      string
	TimeoutSeconds             int
	MCPGlobalSearchConcurrency int
	MCPUserSearchConcurrency   int
	ProxyURL                   string
	ProxyEnabled               bool
	RegistrationMode           RegistrationMode
	Debug                      bool
}
