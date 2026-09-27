// Package netpolicy decides which network destinations checks and webhook
// notifications are allowed to reach. It always refuses link-local and
// cloud-metadata addresses and the configured Docker host; it optionally
// also refuses private, loopback and carrier-grade-NAT ranges.
package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrBlocked is wrapped by every error Check and DialContext return for a
// refused address.
var ErrBlocked = errors.New("target refused by outbound policy")

// dockerCacheTTL is how long a resolved Docker host name's IPs are cached.
const dockerCacheTTL = time.Minute

var (
	cgnat          = netip.MustParsePrefix("100.64.0.0/10")
	metadataAWSv6  = netip.MustParseAddr("fd00:ec2::254")
	metadataAliyun = netip.MustParseAddr("100.100.100.200")
)

// Policy holds the outbound target policy: the always-blocked Docker host
// and the toggle for whether private ranges are additionally refused.
type Policy struct {
	dockerLiteral  netip.Addr // valid only if DOCKER_HOST was a literal IP
	dockerHostname string     // set only if DOCKER_HOST was a resolvable name

	blockPrivate atomic.Bool

	mu           sync.Mutex
	dockerIPs    map[netip.Addr]struct{} // served set: union of the last two successful resolutions
	lastResolved map[netip.Addr]struct{} // most recent successful resolution alone
	expiresAt    time.Time

	// lookupIP resolves dockerHostname; overridable in tests. Defaults to
	// net.LookupIP.
	lookupIP func(host string) ([]net.IP, error)
}

// New builds a Policy from the DOCKER_HOST environment value. Only
// "tcp://host:port" URLs contribute a blocked address; "unix://" sockets and
// an empty value contribute nothing.
func New(dockerHost string) *Policy {
	p := &Policy{lookupIP: net.LookupIP}
	if dockerHost == "" {
		return p
	}
	u, err := url.Parse(dockerHost)
	if err != nil || u.Scheme != "tcp" {
		return p
	}
	host := u.Hostname()
	if host == "" {
		return p
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		p.dockerLiteral = ip
	} else {
		p.dockerHostname = host
	}
	return p
}

// SetBlockPrivate turns blocking of loopback, RFC1918, CGNAT and ULA ranges
// on or off. It is safe to call concurrently with Check and DialContext.
func (p *Policy) SetBlockPrivate(block bool) { p.blockPrivate.Store(block) }

// BlockPrivate reports the current private-range blocking setting.
func (p *Policy) BlockPrivate() bool { return p.blockPrivate.Load() }

// Check reports whether ip is allowed to be dialed. A refused address
// returns an error wrapping ErrBlocked.
func (p *Policy) Check(ip netip.Addr) error {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlocked)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("%w: unspecified address %s", ErrBlocked, ip)
	}
	if ip.IsLinkLocalUnicast() {
		return fmt.Errorf("%w: link-local address %s", ErrBlocked, ip)
	}
	if ip == metadataAWSv6 || ip == metadataAliyun {
		return fmt.Errorf("%w: cloud metadata address %s", ErrBlocked, ip)
	}
	if p.isDockerHost(ip) {
		return fmt.Errorf("%w: docker host address %s", ErrBlocked, ip)
	}
	if p.BlockPrivate() {
		if ip.IsLoopback() {
			return fmt.Errorf("%w: loopback address %s", ErrBlocked, ip)
		}
		if ip.IsPrivate() {
			return fmt.Errorf("%w: private address %s", ErrBlocked, ip)
		}
		if cgnat.Contains(ip) {
			return fmt.Errorf("%w: carrier-grade NAT address %s", ErrBlocked, ip)
		}
	}
	return nil
}

// DialContext dials addr, checking the resolved remote IP against Check at
// connect time (via the dialer's Control hook) so that a DNS answer that
// changes between resolution and connection cannot bypass the policy.
func (p *Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("%w: cannot parse address %q", ErrBlocked, address)
			}
			return p.Check(ip)
		},
	}
	return d.DialContext(ctx, network, addr)
}

// isDockerHost reports whether ip is (one of) the configured Docker host's
// addresses.
func (p *Policy) isDockerHost(ip netip.Addr) bool {
	if p.dockerLiteral.IsValid() && ip == p.dockerLiteral {
		return true
	}
	if p.dockerHostname == "" {
		return false
	}
	_, ok := p.resolvedDockerIPs()[ip]
	return ok
}

// resolvedDockerIPs returns the set of IPs to refuse for the configured
// Docker host name, re-resolving it at most once per dockerCacheTTL. A
// failed re-resolution (transient DNS hiccup or outage) keeps serving the
// last known set rather than clearing the blocklist; a successful one is
// unioned with the previous successful resolution for one extra TTL, so an
// address the host just stopped advertising is still refused briefly.
func (p *Policy) resolvedDockerIPs() map[netip.Addr]struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Now().Before(p.expiresAt) {
		return p.dockerIPs
	}
	p.expiresAt = time.Now().Add(dockerCacheTTL)
	addrs, err := p.lookupIP(p.dockerHostname)
	if err != nil {
		return p.dockerIPs
	}
	current := make(map[netip.Addr]struct{}, len(addrs))
	for _, a := range addrs {
		if ip, ok := netip.AddrFromSlice(a); ok {
			current[ip.Unmap()] = struct{}{}
		}
	}
	union := make(map[netip.Addr]struct{}, len(current)+len(p.lastResolved))
	for ip := range current {
		union[ip] = struct{}{}
	}
	for ip := range p.lastResolved {
		union[ip] = struct{}{}
	}
	p.lastResolved = current
	p.dockerIPs = union
	return union
}
