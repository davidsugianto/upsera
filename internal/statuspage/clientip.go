package statuspage

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// clientKey is the rate-limit key of the client behind r: its IPv4 address,
// or the /64 an IPv6 address belongs to (one household or attacker controls
// a whole /64, so per-address limits on IPv6 are meaningless).
//
// X-Forwarded-For is only believed when the peer is one of
// Options.TrustedProxies. Then the client is the rightmost entry that is not
// itself a trusted proxy: leading entries are client-supplied and can say
// anything. Anything that does not parse falls back to the peer address.
func (s *Service) clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host // not an IP (a unix socket peer, a test): use it as is
	}
	peer = peer.Unmap()
	ip := peer
	if s.isTrustedProxy(peer) {
		if c, ok := s.forwardedClient(r); ok {
			ip = c
		}
	}
	if ip.Is6() {
		p, err := ip.Prefix(64)
		if err == nil {
			return p.String()
		}
	}
	return ip.String()
}

func (s *Service) isTrustedProxy(ip netip.Addr) bool {
	for _, p := range s.opts.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// forwardedClient returns the rightmost X-Forwarded-For address that is not a
// trusted proxy. An entry that is not an IP ends the search without a result.
func (s *Service) forwardedClient(r *http.Request) (netip.Addr, bool) {
	var parts []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		parts = append(parts, strings.Split(line, ",")...)
	}
	for i := len(parts) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return netip.Addr{}, false
		}
		ip = ip.Unmap()
		if !s.isTrustedProxy(ip) {
			return ip, true
		}
	}
	return netip.Addr{}, false
}
