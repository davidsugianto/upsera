package statuspage

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func TestClientKey(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	cases := map[string]struct {
		trusted []netip.Prefix
		remote  string
		xff     []string
		want    string
	}{
		"direct peer":                    {nil, "203.0.113.9:4000", nil, "203.0.113.9"},
		"xff ignored without proxies":    {nil, "203.0.113.9:4000", []string{"198.51.100.7"}, "203.0.113.9"},
		"xff ignored from untrusted":     {trusted, "203.0.113.9:4000", []string{"198.51.100.7"}, "203.0.113.9"},
		"trusted proxy":                  {trusted, "10.1.1.1:4000", []string{"198.51.100.7"}, "198.51.100.7"},
		"leftmost entry is spoofable":    {trusted, "10.1.1.1:4000", []string{"6.6.6.6, 198.51.100.7"}, "198.51.100.7"},
		"trusted hops are skipped":       {trusted, "10.1.1.1:4000", []string{"198.51.100.7, 10.2.2.2"}, "198.51.100.7"},
		"several header lines":           {trusted, "10.1.1.1:4000", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"},
		"garbage falls back to peer":     {trusted, "10.1.1.1:4000", []string{"not-an-ip"}, "10.1.1.1"},
		"garbage at the end is no ip":    {trusted, "10.1.1.1:4000", []string{"198.51.100.7, bogus"}, "10.1.1.1"},
		"no header":                      {trusted, "10.1.1.1:4000", nil, "10.1.1.1"},
		"all hops trusted":               {trusted, "10.1.1.1:4000", []string{"10.3.3.3"}, "10.1.1.1"},
		"ipv6 client is bucketed by /64": {nil, "[2001:db8:1:2:aaaa:bbbb:cccc:1]:4000", nil, "2001:db8:1:2::/64"},
		"ipv6 via proxy":                 {trusted, "[fd00::1]:4000", []string{"2001:db8:1:2:1::9"}, "2001:db8:1:2::/64"},
		"v4-mapped v6 is plain v4":       {nil, "[::ffff:192.0.2.5]:4000", nil, "192.0.2.5"},
		"unparsable peer is kept":        {nil, "weird", nil, "weird"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestService(t, &fakeStore{}, nil, func(o *Options) { o.TrustedProxies = tc.trusted })
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := s.clientKey(r); got != tc.want {
				t.Fatalf("clientKey = %q, want %q", got, tc.want)
			}
		})
	}
	// Two clients in the same /64 share a key; neighbouring /64s do not.
	s := newTestService(t, &fakeStore{}, nil, nil)
	key := func(addr string) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = addr
		return s.clientKey(r)
	}
	if key("[2001:db8:1:2::1]:1") != key("[2001:db8:1:2:ffff::9]:1") || key("[2001:db8:1:2::1]:1") == key("[2001:db8:1:3::1]:1") {
		t.Fatal("IPv6 bucketing is wrong")
	}
}

// Behind a configured proxy every visitor must get their own bucket;
// without one, X-Forwarded-For must not let a client pick its bucket.
func TestSubscribeLimitBehindProxy(t *testing.T) {
	send := func(e *subscribeTestEnv, i int, client string) int {
		rec := e.post("/status/acme/subscribe", url.Values{"email": {"u" + string(rune('a'+i)) + client + "@example.com"}}, func(r *http.Request) {
			r.RemoteAddr = "10.0.0.1:5000"
			r.Header.Set("X-Forwarded-For", client)
		})
		return rec.Code
	}

	t.Run("trusted proxy: one bucket per client", func(t *testing.T) {
		e := newSubscribeTestEnv(t)
		e.svc.opts.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
		for i := 0; i < subscribeIPMax; i++ {
			if code := send(e, i, "198.51.100.1"); code != 200 {
				t.Fatalf("client A request %d: %d", i+1, code)
			}
		}
		if code := send(e, 9, "198.51.100.1"); code != http.StatusTooManyRequests {
			t.Fatalf("client A over its limit: %d, want 429", code)
		}
		if code := send(e, 0, "198.51.100.2"); code != 200 {
			t.Fatalf("client B must not share A's bucket: %d", code)
		}
	})

	t.Run("no trusted proxy: header cannot choose the bucket", func(t *testing.T) {
		e := newSubscribeTestEnv(t)
		for i := 0; i < subscribeIPMax; i++ {
			send(e, i, "198.51.100.1")
		}
		if code := send(e, 0, "198.51.100.77"); code != http.StatusTooManyRequests {
			t.Fatalf("a spoofed X-Forwarded-For escaped the limit: %d", code)
		}
	})
}
