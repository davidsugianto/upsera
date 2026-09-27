package checker

import (
	"net"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/davidsugianto/upsera/internal/model"
)

// dnsRecord is one canned answer served by newTestDNSServer.
type dnsRecord struct {
	name string // without trailing dot
	typ  dnsmessage.Type
	// exactly one of these is set, matching typ
	a   [4]byte
	txt []string
}

// newTestDNSServer starts a UDP DNS server on 127.0.0.1 that answers
// queries from records; anything else gets NXDOMAIN. It returns the
// "host:port" address to configure as a monitor's resolver.
func newTestDNSServer(t *testing.T, records []dnsRecord) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buf[:n]); err != nil {
				continue
			}
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: query.Header.ID, Response: true, RCode: dnsmessage.RCodeNameError},
				Questions: query.Questions,
			}
			if len(query.Questions) == 1 {
				q := query.Questions[0]
				for _, rec := range records {
					if !strings.EqualFold(q.Name.String(), rec.name+".") {
						continue
					}
					// The name exists (regardless of the queried
					// type); a real server answers NOERROR here even
					// with no record of the requested type, reserving
					// NXDOMAIN for a genuinely unknown name.
					resp.Header.RCode = dnsmessage.RCodeSuccess
					if rec.typ != q.Type {
						continue
					}
					header := dnsmessage.ResourceHeader{Name: q.Name, Type: rec.typ, Class: dnsmessage.ClassINET, TTL: 60}
					var body dnsmessage.ResourceBody
					switch rec.typ {
					case dnsmessage.TypeA:
						body = &dnsmessage.AResource{A: rec.a}
					case dnsmessage.TypeTXT:
						body = &dnsmessage.TXTResource{TXT: rec.txt}
					}
					resp.Answers = append(resp.Answers, dnsmessage.Resource{Header: header, Body: body})
				}
			}
			packed, err := resp.Pack()
			if err != nil {
				continue
			}
			_, _ = conn.WriteTo(packed, addr)
		}
	}()

	return conn.LocalAddr().String()
}

func TestDNSRecordMatch(t *testing.T) {
	t.Parallel()
	server := newTestDNSServer(t, []dnsRecord{
		{name: "example.test", typ: dnsmessage.TypeA, a: [4]byte{1, 2, 3, 4}},
	})

	c := newTestChecker()
	m := mustMonitor(t, model.TypeDNS, DNSConfig{Hostname: "example.test", RecordType: "A", Resolver: server, Expected: "1.2.3.4"}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "1.2.3.4") {
		t.Errorf("Message = %q, want it to list the answer", res.Message)
	}
}

func TestDNSExpectedMismatch(t *testing.T) {
	t.Parallel()
	server := newTestDNSServer(t, []dnsRecord{
		{name: "example.test", typ: dnsmessage.TypeA, a: [4]byte{1, 2, 3, 4}},
	})

	c := newTestChecker()
	m := mustMonitor(t, model.TypeDNS, DNSConfig{Hostname: "example.test", RecordType: "A", Resolver: server, Expected: "9.9.9.9"}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown {
		t.Fatalf("Status = %v, want Down (expected value absent)", res.Status)
	}
}

func TestDNSTXTRecord(t *testing.T) {
	t.Parallel()
	server := newTestDNSServer(t, []dnsRecord{
		{name: "example.test", typ: dnsmessage.TypeTXT, txt: []string{"v=upsera1"}},
	})

	c := newTestChecker()
	m := mustMonitor(t, model.TypeDNS, DNSConfig{Hostname: "example.test", RecordType: "TXT", Resolver: server, Expected: "upsera1"}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
}

func TestDNSNXDomain(t *testing.T) {
	t.Parallel()
	server := newTestDNSServer(t, nil) // no records; everything is NXDOMAIN

	c := newTestChecker()
	m := mustMonitor(t, model.TypeDNS, DNSConfig{Hostname: "missing.test", RecordType: "A", Resolver: server}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown {
		t.Fatalf("Status = %v, want Down (NXDOMAIN)", res.Status)
	}
}

func TestDNSResolverBlocked(t *testing.T) {
	t.Parallel()
	c := newTestChecker()
	m := mustMonitor(t, model.TypeDNS, DNSConfig{Hostname: "example.test", RecordType: "A", Resolver: "169.254.169.254:53"}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || !strings.Contains(res.Message, "blocked") {
		t.Fatalf("got status=%v message=%q, want Down mentioning blocked", res.Status, res.Message)
	}
}

func TestDNSCNAMENoRecord(t *testing.T) {
	t.Parallel()
	// Only an A record for the name: net.Resolver.LookupCNAME (and our
	// direct exchange) return the queried name itself, fully qualified,
	// rather than an error, when there is no CNAME record.
	server := newTestDNSServer(t, []dnsRecord{
		{name: "example.test", typ: dnsmessage.TypeA, a: [4]byte{1, 2, 3, 4}},
	})

	c := newTestChecker()
	m := mustMonitor(t, model.TypeDNS, DNSConfig{Hostname: "example.test", RecordType: "CNAME", Resolver: server}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || !strings.Contains(res.Message, "no CNAME record") {
		t.Fatalf("got status=%v message=%q, want Down mentioning no CNAME record", res.Status, res.Message)
	}
}

func TestDNSResolverBypassesHostsFile(t *testing.T) {
	t.Parallel()
	// A configured Resolver that NXDOMAINs everything must actually be
	// consulted, not bypassed by /etc/hosts' "localhost" entry.
	server := newTestDNSServer(t, nil)

	c := newTestChecker()
	m := mustMonitor(t, model.TypeDNS, DNSConfig{Hostname: "localhost", RecordType: "A", Resolver: server}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown {
		t.Fatalf("got status=%v message=%q, want Down: configured resolver must be consulted instead of /etc/hosts", res.Status, res.Message)
	}
}
