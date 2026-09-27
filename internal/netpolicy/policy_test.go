package netpolicy

import (
	"errors"
	"net"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestCheckAlwaysBlocked(t *testing.T) {
	t.Parallel()
	p := New("")
	cases := []string{
		"169.254.1.1",            // IPv4 link-local / metadata range
		"169.254.169.254",        // cloud metadata
		"fe80::1",                // IPv6 link-local
		"::ffff:169.254.169.254", // mapped metadata address
		"fd00:ec2::254",          // AWS IPv6 metadata
		"100.100.100.200",        // Alibaba metadata
		"0.0.0.0",
		"::",
	}
	for _, s := range cases {
		ip := netip.MustParseAddr(s)
		if err := p.Check(ip); err == nil || !errors.Is(err, ErrBlocked) {
			t.Errorf("Check(%s) = %v, want ErrBlocked", s, err)
		}
	}
}

func TestCheckAllowedByDefault(t *testing.T) {
	t.Parallel()
	p := New("")
	cases := []string{
		"8.8.8.8",
		"127.0.0.1",
		"::1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"100.64.0.1",
		"fc00::1",
		"::ffff:8.8.8.8",
	}
	for _, s := range cases {
		ip := netip.MustParseAddr(s)
		if err := p.Check(ip); err != nil {
			t.Errorf("Check(%s) = %v, want nil (private allowed by default)", s, err)
		}
	}
}

func TestBlockPrivateToggle(t *testing.T) {
	t.Parallel()
	p := New("")
	blocked := []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "100.64.0.1", "fc00::1"}
	for _, s := range blocked {
		if err := p.Check(netip.MustParseAddr(s)); err != nil {
			t.Fatalf("Check(%s) before enabling BlockPrivate = %v, want nil", s, err)
		}
	}

	p.SetBlockPrivate(true)
	if !p.BlockPrivate() {
		t.Fatal("BlockPrivate() = false after SetBlockPrivate(true)")
	}
	for _, s := range blocked {
		if err := p.Check(netip.MustParseAddr(s)); err == nil || !errors.Is(err, ErrBlocked) {
			t.Errorf("Check(%s) with BlockPrivate on = %v, want ErrBlocked", s, err)
		}
	}
	// Public addresses stay allowed.
	if err := p.Check(netip.MustParseAddr("8.8.8.8")); err != nil {
		t.Errorf("Check(8.8.8.8) with BlockPrivate on = %v, want nil", err)
	}

	p.SetBlockPrivate(false)
	for _, s := range blocked {
		if err := p.Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("Check(%s) after disabling BlockPrivate = %v, want nil", s, err)
		}
	}
}

func TestDockerHostLiteral(t *testing.T) {
	t.Parallel()
	p := New("tcp://198.51.100.5:2375")
	if err := p.Check(netip.MustParseAddr("198.51.100.5")); err == nil || !errors.Is(err, ErrBlocked) {
		t.Errorf("Check(docker literal) = %v, want ErrBlocked", err)
	}
	if err := p.Check(netip.MustParseAddr("198.51.100.6")); err != nil {
		t.Errorf("Check(other IP) = %v, want nil", err)
	}
}

func TestResolvedDockerIPsKeepsLastKnownOnLookupFailure(t *testing.T) {
	t.Parallel()
	p := New("tcp://docker.internal:2375")
	target := netip.MustParseAddr("10.0.0.5")
	calls := 0
	p.lookupIP = func(string) ([]net.IP, error) {
		calls++
		if calls == 1 {
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		}
		return nil, errors.New("lookup failed")
	}

	if err := p.Check(target); err == nil || !errors.Is(err, ErrBlocked) {
		t.Fatalf("Check(%s) after first resolution = %v, want ErrBlocked", target, err)
	}

	p.expiresAt = time.Time{} // force re-resolution; this one fails
	if err := p.Check(target); err == nil || !errors.Is(err, ErrBlocked) {
		t.Fatalf("Check(%s) after failed re-resolution = %v, want still ErrBlocked (keep last known IPs)", target, err)
	}
	if calls != 2 {
		t.Fatalf("lookupIP called %d times, want 2", calls)
	}
}

func TestResolvedDockerIPsUnionsWithPreviousForOneTTL(t *testing.T) {
	t.Parallel()
	p := New("tcp://docker.internal:2375")
	oldIP := netip.MustParseAddr("10.0.0.5")
	newIP := netip.MustParseAddr("10.0.0.6")
	calls := 0
	p.lookupIP = func(string) ([]net.IP, error) {
		calls++
		if calls == 1 {
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		}
		return []net.IP{net.ParseIP("10.0.0.6")}, nil
	}

	if err := p.Check(oldIP); err == nil || !errors.Is(err, ErrBlocked) {
		t.Fatalf("Check(old) first resolution = %v, want ErrBlocked", err)
	}

	p.expiresAt = time.Time{} // force re-resolution: now advertises newIP only
	if err := p.Check(oldIP); err == nil || !errors.Is(err, ErrBlocked) {
		t.Fatalf("Check(old) right after rotation = %v, want still ErrBlocked (union with previous resolution for one TTL)", err)
	}
	if err := p.Check(newIP); err == nil || !errors.Is(err, ErrBlocked) {
		t.Fatalf("Check(new) = %v, want ErrBlocked", err)
	}

	p.expiresAt = time.Time{} // one more TTL: oldIP should finally age out
	if err := p.Check(oldIP); err != nil {
		t.Fatalf("Check(old) after two rotations = %v, want nil (aged out)", err)
	}
}

func TestDockerHostNameResolved(t *testing.T) {
	t.Parallel()
	p := New("tcp://localhost:2375")
	// "localhost" resolves via the system resolver without network access;
	// its loopback IPs must be blocked as the docker host regardless of
	// BlockPrivate.
	for _, s := range []string{"127.0.0.1", "::1"} {
		ip := netip.MustParseAddr(s)
		if err := p.isDockerHostCheck(ip); err != nil {
			t.Skipf("localhost did not resolve to %s in this sandbox: %v", s, err)
		}
	}
	if err := p.Check(netip.MustParseAddr("127.0.0.1")); err == nil || !errors.Is(err, ErrBlocked) {
		t.Errorf("Check(127.0.0.1) with docker host localhost = %v, want ErrBlocked", err)
	}
	// Calling again exercises the cache path.
	if err := p.Check(netip.MustParseAddr("127.0.0.1")); err == nil || !errors.Is(err, ErrBlocked) {
		t.Errorf("Check(127.0.0.1) second call = %v, want ErrBlocked", err)
	}
}

// isDockerHostCheck is a tiny test helper reproducing whether ip is
// recognized as the resolved docker host, returning an error (not a bool) so
// the caller can skip when the environment's resolver doesn't cooperate.
func (p *Policy) isDockerHostCheck(ip netip.Addr) error {
	if p.isDockerHost(ip) {
		return nil
	}
	return errors.New("not resolved as docker host")
}

func TestDockerHostEmpty(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"", "unix:///var/run/docker.sock"} {
		p := New(host)
		if err := p.Check(netip.MustParseAddr("8.8.8.8")); err != nil {
			t.Errorf("New(%q): Check(8.8.8.8) = %v, want nil", host, err)
		}
	}
}

func TestDialContextRefused(t *testing.T) {
	t.Parallel()
	p := New("")
	_, err := p.DialContext(t.Context(), "tcp", "169.254.169.254:80")
	if err == nil || !errors.Is(err, ErrBlocked) {
		t.Fatalf("DialContext(metadata IP) = %v, want ErrBlocked", err)
	}
}

func TestDialContextLoopback(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nil)
	defer srv.Close()

	p := New("")
	conn, err := p.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("DialContext(loopback) with private allowed = %v, want success", err)
	}
	conn.Close()

	p.SetBlockPrivate(true)
	_, err = p.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err == nil || !errors.Is(err, ErrBlocked) {
		t.Fatalf("DialContext(loopback) with BlockPrivate on = %v, want ErrBlocked", err)
	}
}
