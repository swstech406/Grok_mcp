package ratelimit

import (
	"errors"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func newTrustedProxyResolver() *ClientIPResolver {
	return NewClientIPResolverWithConfig(ClientIPResolverConfig{
		Mode: ClientIPModeTrustedProxy,
		TrustedProxyPrefixes: []netip.Prefix{
			netip.MustParsePrefix("192.0.2.0/24"),
			netip.MustParsePrefix("2001:db8:ffff::/48"),
		},
	})
}

func TestClientIPResolverDirectModeUsesRemoteAddressAndIgnoresForwardingHeaders(t *testing.T) {
	resolver := NewClientIPResolver()
	testCases := []struct {
		name          string
		remoteAddress string
		expected      string
	}{
		{name: "IPv4 with port", remoteAddress: "198.51.100.10:443", expected: "198.51.100.10"},
		{name: "IPv6 with port", remoteAddress: "[2001:db8::10]:443", expected: "2001:db8::10"},
		{name: "bare IPv4", remoteAddress: "198.51.100.11", expected: "198.51.100.11"},
		{name: "mapped IPv6", remoteAddress: "[::ffff:198.51.100.12]:443", expected: "198.51.100.12"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/", nil)
			request.RemoteAddr = testCase.remoteAddress
			request.Header.Set("X-Real-IP", "203.0.113.20")
			request.Header.Set("X-Forwarded-For", "not-an-ip")

			address, err := resolver.ResolveAddress(request)
			if err != nil {
				t.Fatalf("ResolveAddress returned error: %v", err)
			}
			if got := address.String(); got != testCase.expected {
				t.Fatalf("resolved address = %q, want %q", got, testCase.expected)
			}
		})
	}
}

func TestClientIPResolverDirectModeRejectsMalformedRemoteAddress(t *testing.T) {
	resolver := NewClientIPResolver()
	for _, remoteAddress := range []string{"", "proxy.internal:443", "198.51.100.10:bad", "[fe80::1%eth0]:443"} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = remoteAddress
		if _, err := resolver.ResolveAddress(request); !errors.Is(err, ErrInvalidClientIPIdentity) {
			t.Fatalf("RemoteAddr %q error = %v, want %v", remoteAddress, err, ErrInvalidClientIPIdentity)
		}
	}
}

func TestClientIPResolverTrustedProxyReturnsCanonicalForwardedAddress(t *testing.T) {
	resolver := newTrustedProxyResolver()
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.20:443"
	request.Header.Set("X-Forwarded-For", "::ffff:198.51.100.10, 203.0.113.20")

	address, err := resolver.ResolveAddress(request)
	if err != nil {
		t.Fatalf("ResolveAddress returned error: %v", err)
	}
	if got, want := address.String(), "198.51.100.10"; got != want {
		t.Fatalf("resolved address = %q, want %q", got, want)
	}

	request.Header.Set("X-Forwarded-For", "192.0.2.99")
	if got, want := address.String(), "198.51.100.10"; got != want {
		t.Fatalf("previously resolved address changed after header mutation: got %q, want %q", got, want)
	}
}

func TestClientIPResolverTrustedProxyAllowsMatchingHeaders(t *testing.T) {
	resolver := newTrustedProxyResolver()
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[2001:db8:ffff::10]:443"
	request.Header.Set("X-Real-IP", "2001:db8::1")
	request.Header.Set("X-Forwarded-For", "2001:0db8:0:0:0:0:0:1, 203.0.113.20")

	address, err := resolver.ResolveAddress(request)
	if err != nil {
		t.Fatalf("ResolveAddress returned error: %v", err)
	}
	if got, want := address.String(), "2001:db8::1"; got != want {
		t.Fatalf("resolved address = %q, want %q", got, want)
	}
}

func TestClientIPResolverTrustedProxyRejectsUntrustedOrMissingIdentity(t *testing.T) {
	resolver := newTrustedProxyResolver()

	untrustedRequest := httptest.NewRequest("GET", "/", nil)
	untrustedRequest.RemoteAddr = "203.0.113.10:443"
	untrustedRequest.Header.Set("X-Forwarded-For", "198.51.100.10")
	if _, err := resolver.ResolveAddress(untrustedRequest); !errors.Is(err, ErrUntrustedClientIPPeer) {
		t.Fatalf("untrusted peer error = %v, want %v", err, ErrUntrustedClientIPPeer)
	}

	missingHeaderRequest := httptest.NewRequest("GET", "/", nil)
	missingHeaderRequest.RemoteAddr = "192.0.2.10:443"
	if _, err := resolver.ResolveAddress(missingHeaderRequest); !errors.Is(err, ErrInvalidForwardedClientIPHeaders) {
		t.Fatalf("missing forwarded identity error = %v, want %v", err, ErrInvalidForwardedClientIPHeaders)
	}
}

func TestClientIPResolverTrustedProxyRejectsConflictingHeaders(t *testing.T) {
	resolver := newTrustedProxyResolver()
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:443"
	request.Header.Set("X-Real-IP", "198.51.100.10")
	request.Header.Set("X-Forwarded-For", "198.51.100.11")

	_, err := resolver.ResolveAddress(request)
	if !errors.Is(err, ErrInvalidForwardedClientIPHeaders) {
		t.Fatalf("ResolveAddress error = %v, want %v", err, ErrInvalidForwardedClientIPHeaders)
	}
}

func TestClientIPResolverTrustedProxyRejectsDuplicateHeaderValuesAndNames(t *testing.T) {
	resolver := newTrustedProxyResolver()

	duplicateValuesRequest := httptest.NewRequest("GET", "/", nil)
	duplicateValuesRequest.RemoteAddr = "192.0.2.10:443"
	duplicateValuesRequest.Header["X-Forwarded-For"] = []string{"198.51.100.10", "198.51.100.11"}
	if _, err := resolver.ResolveAddress(duplicateValuesRequest); !errors.Is(err, ErrInvalidForwardedClientIPHeaders) {
		t.Fatalf("duplicate values error = %v, want %v", err, ErrInvalidForwardedClientIPHeaders)
	}

	duplicateNamesRequest := httptest.NewRequest("GET", "/", nil)
	duplicateNamesRequest.RemoteAddr = "192.0.2.10:443"
	duplicateNamesRequest.Header["X-Real-IP"] = []string{"198.51.100.10"}
	duplicateNamesRequest.Header["x-real-ip"] = []string{"198.51.100.10"}
	if _, err := resolver.ResolveAddress(duplicateNamesRequest); !errors.Is(err, ErrInvalidForwardedClientIPHeaders) {
		t.Fatalf("duplicate names error = %v, want %v", err, ErrInvalidForwardedClientIPHeaders)
	}
}

func TestClientIPResolverTrustedProxyEnforcesHeaderLengthAndHopLimits(t *testing.T) {
	resolver := NewClientIPResolverWithConfig(ClientIPResolverConfig{
		Mode:                           ClientIPModeTrustedProxy,
		TrustedProxyPrefixes:           []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		MaximumRealIPHeaderBytes:       12,
		MaximumForwardedForHeaderBytes: 128,
		MaximumForwardedHops:           2,
	})

	oversizedRealIPRequest := httptest.NewRequest("GET", "/", nil)
	oversizedRealIPRequest.RemoteAddr = "192.0.2.10:443"
	oversizedRealIPRequest.Header.Set("X-Real-IP", "198.51.100.100")
	if _, err := resolver.ResolveAddress(oversizedRealIPRequest); !errors.Is(err, ErrInvalidForwardedClientIPHeaders) {
		t.Fatalf("oversized X-Real-IP error = %v, want %v", err, ErrInvalidForwardedClientIPHeaders)
	}

	tooManyHopsRequest := httptest.NewRequest("GET", "/", nil)
	tooManyHopsRequest.RemoteAddr = "192.0.2.10:443"
	tooManyHopsRequest.Header.Set("X-Forwarded-For", "198.51.100.10,198.51.100.11,198.51.100.12")
	if _, err := resolver.ResolveAddress(tooManyHopsRequest); !errors.Is(err, ErrInvalidForwardedClientIPHeaders) {
		t.Fatalf("too many hops error = %v, want %v", err, ErrInvalidForwardedClientIPHeaders)
	}

	headerLimitedResolver := NewClientIPResolverWithConfig(ClientIPResolverConfig{
		Mode:                           ClientIPModeTrustedProxy,
		TrustedProxyPrefixes:           []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		MaximumForwardedForHeaderBytes: 20,
		MaximumForwardedHops:           16,
	})
	oversizedForwardedForRequest := httptest.NewRequest("GET", "/", nil)
	oversizedForwardedForRequest.RemoteAddr = "192.0.2.10:443"
	oversizedForwardedForRequest.Header.Set("X-Forwarded-For", "198.51.100.10, 198.51.100.11")
	if _, err := headerLimitedResolver.ResolveAddress(oversizedForwardedForRequest); !errors.Is(err, ErrInvalidForwardedClientIPHeaders) {
		t.Fatalf("oversized X-Forwarded-For error = %v, want %v", err, ErrInvalidForwardedClientIPHeaders)
	}
}

func TestClientIPResolverTrustedProxyRejectsMalformedOrEmptyHops(t *testing.T) {
	resolver := newTrustedProxyResolver()
	testValues := []string{
		"",
		"unknown",
		"198.51.100.10,,203.0.113.20",
		"198.51.100.10, invalid",
		"fe80::1%eth0",
	}

	for _, testValue := range testValues {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = "192.0.2.10:443"
		request.Header.Set("X-Forwarded-For", testValue)
		if _, err := resolver.ResolveAddress(request); !errors.Is(err, ErrInvalidForwardedClientIPHeaders) {
			t.Fatalf("X-Forwarded-For %q error = %v, want %v", testValue, err, ErrInvalidForwardedClientIPHeaders)
		}
	}
}
