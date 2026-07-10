package ratelimit

import (
	"net"
	"net/http"
	"strings"
)

// ClientIPResolver resolves the original client IP while only trusting
// forwarding headers from explicitly configured reverse proxy networks.
type ClientIPResolver struct {
	trustedProxies []*net.IPNet
}

// NewClientIPResolver creates an immutable trusted-proxy-aware IP resolver.
func NewClientIPResolver(trustedProxies []*net.IPNet) *ClientIPResolver {
	return &ClientIPResolver{trustedProxies: cloneIPNetworks(trustedProxies)}
}

// Resolve returns the request's effective client IP. Forwarding headers are
// ignored unless the immediate peer belongs to a configured trusted network.
func (resolver *ClientIPResolver) Resolve(request *http.Request) string {
	if request == nil || strings.TrimSpace(request.RemoteAddr) == "" {
		return "unknown"
	}

	remoteHost := clientAddressForLimit(request.RemoteAddr)
	if resolver == nil || len(resolver.trustedProxies) == 0 || !ipHostInNetworks(remoteHost, resolver.trustedProxies) {
		return remoteHost
	}

	// The immediate peer is trusted, so forwarding headers may identify the
	// original client without allowing direct clients to spoof their bucket.
	if realIP := strings.TrimSpace(request.Header.Get("X-Real-IP")); realIP != "" {
		if host := normalizeIPHost(realIP); host != "" {
			return host
		}
	}
	if forwardedFor := strings.TrimSpace(request.Header.Get("X-Forwarded-For")); forwardedFor != "" {
		// Prefer the rightmost untrusted hop so intermediate trusted proxies are skipped.
		forwardedHops := strings.Split(forwardedFor, ",")
		for index := len(forwardedHops) - 1; index >= 0; index-- {
			host := normalizeIPHost(forwardedHops[index])
			if host == "" {
				continue
			}
			if !ipHostInNetworks(host, resolver.trustedProxies) {
				return host
			}
		}
		// If every hop is trusted, the leftmost valid address is the original client.
		for _, forwardedHop := range forwardedHops {
			if host := normalizeIPHost(forwardedHop); host != "" {
				return host
			}
		}
	}
	return remoteHost
}

func cloneIPNetworks(networks []*net.IPNet) []*net.IPNet {
	if len(networks) == 0 {
		return nil
	}

	clonedNetworks := make([]*net.IPNet, 0, len(networks))
	for _, network := range networks {
		if network == nil {
			continue
		}
		ipCopy := append(net.IP(nil), network.IP...)
		maskCopy := append(net.IPMask(nil), network.Mask...)
		clonedNetworks = append(clonedNetworks, &net.IPNet{IP: ipCopy, Mask: maskCopy})
	}
	return clonedNetworks
}

func clientAddressForLimit(remoteAddress string) string {
	trimmedAddress := strings.TrimSpace(remoteAddress)
	host, _, err := net.SplitHostPort(trimmedAddress)
	if err != nil || host == "" {
		return trimmedAddress
	}
	return host
}

func normalizeIPHost(rawHost string) string {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return ""
	}
	// X-Forwarded-For entries are usually bare IPs; tolerate host:port.
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip == nil {
		return ""
	}
	return host
}

func ipHostInNetworks(host string, networks []*net.IPNet) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	for _, network := range networks {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
