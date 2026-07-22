package ratelimit

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

const (
	defaultMaximumRealIPHeaderBytes       = 64
	defaultMaximumForwardedForHeaderBytes = 512
	defaultMaximumForwardedHops           = 16
)

// ClientIPMode defines whether request identity comes from the direct peer or
// from forwarding headers supplied by an authenticated reverse proxy peer.
type ClientIPMode string

const (
	ClientIPModeDirect       ClientIPMode = "direct"
	ClientIPModeTrustedProxy ClientIPMode = "trusted_proxy"
)

// ErrInvalidClientIPIdentity indicates that a request did not contain a
// complete, canonical client-IP identity for the configured network mode.
var ErrInvalidClientIPIdentity = errors.New("invalid client IP identity")

// ErrInvalidForwardedClientIPHeaders indicates that forwarded client address
// headers were present but malformed, duplicated, conflicting, or oversized.
var ErrInvalidForwardedClientIPHeaders = errors.New("invalid forwarded client IP headers")

// ErrUntrustedClientIPPeer indicates that forwarding headers were presented by
// an immediate peer outside the configured trusted proxy prefixes.
var ErrUntrustedClientIPPeer = errors.New("untrusted client IP peer")

// ClientIPResolverConfig bounds the amount of forwarded-header input parsed
// for one request. Zero values use conservative defaults.
type ClientIPResolverConfig struct {
	Mode                           ClientIPMode
	TrustedProxyPrefixes           []netip.Prefix
	MaximumRealIPHeaderBytes       int
	MaximumForwardedForHeaderBytes int
	MaximumForwardedHops           int
}

// ClientIPResolver resolves a canonical client IP from the authenticated
// network boundary selected at startup.
type ClientIPResolver struct {
	mode                           ClientIPMode
	trustedProxyPrefixes           []netip.Prefix
	maximumRealIPHeaderBytes       int
	maximumForwardedForHeaderBytes int
	maximumForwardedHops           int
}

// NewClientIPResolver creates a direct-peer resolver. This safe default ignores
// all forwarding headers and always derives identity from RemoteAddr.
func NewClientIPResolver() *ClientIPResolver {
	return NewClientIPResolverWithConfig(ClientIPResolverConfig{})
}

// NewClientIPResolverWithConfig creates a resolver with an explicit network
// trust mode and bounded forwarding-header parsing.
func NewClientIPResolverWithConfig(config ClientIPResolverConfig) *ClientIPResolver {
	if config.Mode != ClientIPModeTrustedProxy {
		config.Mode = ClientIPModeDirect
	}
	if config.MaximumRealIPHeaderBytes <= 0 {
		config.MaximumRealIPHeaderBytes = defaultMaximumRealIPHeaderBytes
	}
	if config.MaximumForwardedForHeaderBytes <= 0 {
		config.MaximumForwardedForHeaderBytes = defaultMaximumForwardedForHeaderBytes
	}
	if config.MaximumForwardedHops <= 0 {
		config.MaximumForwardedHops = defaultMaximumForwardedHops
	}
	return &ClientIPResolver{
		mode:                           config.Mode,
		trustedProxyPrefixes:           append([]netip.Prefix(nil), config.TrustedProxyPrefixes...),
		maximumRealIPHeaderBytes:       config.MaximumRealIPHeaderBytes,
		maximumForwardedForHeaderBytes: config.MaximumForwardedForHeaderBytes,
		maximumForwardedHops:           config.MaximumForwardedHops,
	}
}

// Resolve returns a canonical client IP, or an empty string when request
// identity fails validation. Callers that need the failure category should use
// ResolveAddress.
func (resolver *ClientIPResolver) Resolve(request *http.Request) string {
	address, err := resolver.ResolveAddress(request)
	if err != nil || !address.IsValid() {
		return ""
	}
	return address.String()
}

// ResolveAddress authenticates the immediate peer before selecting request
// identity. Direct mode ignores forwarding headers. Trusted-proxy mode accepts
// them only from a configured immediate peer and requires a forwarding value.
func (resolver *ClientIPResolver) ResolveAddress(request *http.Request) (netip.Addr, error) {
	if request == nil {
		return netip.Addr{}, ErrInvalidClientIPIdentity
	}

	peerAddress, err := parseRemoteAddress(request.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	if resolver == nil || resolver.mode != ClientIPModeTrustedProxy {
		return peerAddress, nil
	}
	if !resolver.isTrustedProxy(peerAddress) {
		return netip.Addr{}, ErrUntrustedClientIPPeer
	}

	realIPValue, hasRealIP, err := readSingleHeaderValue(request.Header, "X-Real-IP")
	if err != nil {
		return netip.Addr{}, err
	}
	forwardedForValue, hasForwardedFor, err := readSingleHeaderValue(request.Header, "X-Forwarded-For")
	if err != nil {
		return netip.Addr{}, err
	}
	if !hasRealIP && !hasForwardedFor {
		return netip.Addr{}, ErrInvalidForwardedClientIPHeaders
	}

	var realIPAddress netip.Addr
	if hasRealIP {
		if len(realIPValue) > resolver.maximumRealIPHeaderBytes {
			return netip.Addr{}, ErrInvalidForwardedClientIPHeaders
		}
		realIPAddress, err = parseForwardedAddress(realIPValue)
		if err != nil {
			return netip.Addr{}, err
		}
	}

	var firstForwardedAddress netip.Addr
	if hasForwardedFor {
		if len(forwardedForValue) > resolver.maximumForwardedForHeaderBytes {
			return netip.Addr{}, ErrInvalidForwardedClientIPHeaders
		}
		firstForwardedAddress, err = parseForwardedForAddresses(
			forwardedForValue,
			resolver.maximumForwardedHops,
		)
		if err != nil {
			return netip.Addr{}, err
		}
	}

	if hasRealIP && hasForwardedFor && realIPAddress != firstForwardedAddress {
		return netip.Addr{}, ErrInvalidForwardedClientIPHeaders
	}
	if hasRealIP {
		return realIPAddress, nil
	}
	return firstForwardedAddress, nil
}

func (resolver *ClientIPResolver) isTrustedProxy(peerAddress netip.Addr) bool {
	for _, trustedProxyPrefix := range resolver.trustedProxyPrefixes {
		if trustedProxyPrefix.Contains(peerAddress) {
			return true
		}
	}
	return false
}

func parseRemoteAddress(remoteAddress string) (netip.Addr, error) {
	addressText := strings.TrimSpace(remoteAddress)
	if addressText == "" {
		return netip.Addr{}, ErrInvalidClientIPIdentity
	}

	if addressPort, err := netip.ParseAddrPort(addressText); err == nil {
		address := addressPort.Addr()
		if !address.IsValid() || address.Zone() != "" {
			return netip.Addr{}, ErrInvalidClientIPIdentity
		}
		return address.Unmap(), nil
	}

	address, err := netip.ParseAddr(addressText)
	if err != nil || !address.IsValid() || address.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("%w: malformed RemoteAddr", ErrInvalidClientIPIdentity)
	}
	return address.Unmap(), nil
}

func readSingleHeaderValue(header http.Header, targetName string) (string, bool, error) {
	found := false
	value := ""
	for headerName, headerValues := range header {
		if !strings.EqualFold(headerName, targetName) {
			continue
		}
		if found || len(headerValues) != 1 {
			return "", true, ErrInvalidForwardedClientIPHeaders
		}
		found = true
		value = headerValues[0]
	}
	return value, found, nil
}

func parseForwardedForAddresses(value string, maximumHops int) (netip.Addr, error) {
	if maximumHops <= 0 {
		return netip.Addr{}, ErrInvalidForwardedClientIPHeaders
	}

	var firstAddress netip.Addr
	hopStart := 0
	hopCount := 0
	for {
		hopCount++
		if hopCount > maximumHops {
			return netip.Addr{}, ErrInvalidForwardedClientIPHeaders
		}

		remainingValue := value[hopStart:]
		separatorOffset := strings.IndexByte(remainingValue, ',')
		hopEnd := len(value)
		if separatorOffset >= 0 {
			hopEnd = hopStart + separatorOffset
		}

		address, err := parseForwardedAddress(value[hopStart:hopEnd])
		if err != nil {
			return netip.Addr{}, err
		}
		if hopCount == 1 {
			firstAddress = address
		}

		if separatorOffset < 0 {
			return firstAddress, nil
		}
		hopStart = hopEnd + 1
	}
}

func parseForwardedAddress(rawAddress string) (netip.Addr, error) {
	addressText := strings.TrimSpace(rawAddress)
	if addressText == "" {
		return netip.Addr{}, ErrInvalidForwardedClientIPHeaders
	}

	address, err := netip.ParseAddr(addressText)
	if err != nil {
		if addressPort, addressPortError := netip.ParseAddrPort(addressText); addressPortError == nil {
			address = addressPort.Addr()
			err = nil
		} else if strings.HasPrefix(addressText, "[") && strings.HasSuffix(addressText, "]") {
			address, err = netip.ParseAddr(addressText[1 : len(addressText)-1])
		}
	}
	if err != nil || !address.IsValid() || address.Zone() != "" {
		return netip.Addr{}, ErrInvalidForwardedClientIPHeaders
	}
	return address.Unmap(), nil
}
