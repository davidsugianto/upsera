package checker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestHTTPStatusAccepted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
	if res.Latency <= 0 {
		t.Error("Latency not measured")
	}
}

func TestHTTPStatusMismatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || res.Message != "HTTP 503" {
		t.Fatalf("got status=%v message=%q, want Down 'HTTP 503'", res.Status, res.Message)
	}
}

func TestHTTPStatusRangeAccepted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL, AcceptedStatus: []string{"404"}}, 5)
	if res := c.Check(t.Context(), m); res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
}

func TestHTTPRedirectsFollowed(t *testing.T) {
	t.Parallel()
	var final *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/hop", http.StatusFound)
	})
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/end", http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	final = httptest.NewServer(mux)
	defer final.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: final.URL + "/start", MaxRedirects: new(2)}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up (redirects followed)", res.Status, res.Message)
	}
}

func TestHTTPRedirectsNotFollowed(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL, MaxRedirects: new(0)}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || res.Message != "HTTP 302" {
		t.Fatalf("got status=%v message=%q, want Down 'HTTP 302' (redirect not followed)", res.Status, res.Message)
	}

	m2 := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL, MaxRedirects: new(0), AcceptedStatus: []string{"300-399"}}, 5)
	if res := c.Check(t.Context(), m2); res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up (302 accepted directly)", res.Status, res.Message)
	}
}

func TestHTTPKeywordFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "system is OK today")
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeKeyword, HTTPConfig{URL: srv.URL, Keyword: "ok"}, 5)
	if res := c.Check(t.Context(), m); res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
}

func TestHTTPKeywordNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "system is down")
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeKeyword, HTTPConfig{URL: srv.URL, Keyword: "ok"}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || res.Message != `keyword "ok" not found` {
		t.Fatalf("got status=%v message=%q, want Down `keyword \"ok\" not found`", res.Status, res.Message)
	}
	if strings.Contains(res.Message, "system is down") {
		t.Error("message must never contain response body")
	}
}

func TestHTTPKeywordInverted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "everything is fine")
	}))
	defer srv.Close()

	c := newTestChecker()
	// Keyword present, inverted -> Down.
	m := mustMonitor(t, model.TypeKeyword, HTTPConfig{URL: srv.URL, Keyword: "fine", InvertKeyword: true}, 5)
	if res := c.Check(t.Context(), m); res.Status != model.StatusDown {
		t.Fatalf("Status = %v, want Down (inverted, keyword present)", res.Status)
	}
	// Keyword absent, inverted -> Up.
	m2 := mustMonitor(t, model.TypeKeyword, HTTPConfig{URL: srv.URL, Keyword: "broken", InvertKeyword: true}, 5)
	if res := c.Check(t.Context(), m2); res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up (inverted, keyword absent)", res.Status, res.Message)
	}
}

func TestHTTPKeywordCaseSensitivity(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "Status: OK")
	}))
	defer srv.Close()

	c := newTestChecker()
	// Default (case-insensitive): "ok" matches "OK".
	m := mustMonitor(t, model.TypeKeyword, HTTPConfig{URL: srv.URL, Keyword: "ok"}, 5)
	if res := c.Check(t.Context(), m); res.Status != model.StatusUp {
		t.Fatalf("Status = %v, want Up (case-insensitive match)", res.Status)
	}
	// Case-sensitive: "ok" does not match "OK".
	m2 := mustMonitor(t, model.TypeKeyword, HTTPConfig{URL: srv.URL, Keyword: "ok", CaseSensitive: true}, 5)
	if res := c.Check(t.Context(), m2); res.Status != model.StatusDown {
		t.Fatalf("Status = %v, want Down (case-sensitive mismatch)", res.Status)
	}
}

func TestHTTPHeadersMethodBody(t *testing.T) {
	t.Parallel()
	var gotMethod, gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Probe")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{
		URL:     srv.URL,
		Method:  http.MethodPost,
		Headers: map[string]string{"X-Probe": "upsera"},
		Body:    "ping",
	}, 5)
	if res := c.Check(t.Context(), m); res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotHeader != "upsera" {
		t.Errorf("X-Probe header = %q, want upsera", gotHeader)
	}
	if gotBody != "ping" {
		t.Errorf("body = %q, want ping", gotBody)
	}
}

func TestHTTPTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL}, 1)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || res.Message != "timeout after 1s" {
		t.Fatalf("got status=%v message=%q, want Down 'timeout after 1s'", res.Status, res.Message)
	}
}

func TestHTTPTLSInvalidByDefault(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown {
		t.Fatalf("Status = %v, Message = %q, want Down (untrusted cert)", res.Status, res.Message)
	}
	if res.TLSExpiresAt != nil {
		t.Error("TLSExpiresAt should be nil when the handshake failed")
	}
}

func TestHTTPTLSIgnoredSetsExpiry(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL, IgnoreTLSErrors: true}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up (ignoring TLS errors)", res.Status, res.Message)
	}
	if res.TLSExpiresAt == nil {
		t.Fatal("TLSExpiresAt not set")
	}
}

func TestHTTPTLSExpiredCertStillSetsExpiry(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	cert, notAfter := expiredCert(t)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: srv.URL, IgnoreTLSErrors: true}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up (ignoring TLS errors)", res.Status, res.Message)
	}
	if res.TLSExpiresAt == nil || !res.TLSExpiresAt.Equal(notAfter) {
		t.Fatalf("TLSExpiresAt = %v, want %v", res.TLSExpiresAt, notAfter)
	}
}

func TestHTTPBlockedTarget(t *testing.T) {
	t.Parallel()
	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: "http://169.254.169.254/"}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || !strings.Contains(res.Message, "blocked") {
		t.Fatalf("got status=%v message=%q, want Down mentioning blocked", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "link-local") {
		t.Errorf("Message = %q, want it to keep the policy reason (link-local)", res.Message)
	}
}

func TestHTTPConnectionRefused(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: "http://" + addr + "/"}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || res.Message != "connection refused" {
		t.Fatalf("got status=%v message=%q, want Down 'connection refused'", res.Status, res.Message)
	}
}

func TestHTTPCustomHostHeader(t *testing.T) {
	t.Parallel()
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestChecker()
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{
		URL:     srv.URL,
		Headers: map[string]string{"Host": "custom.example"},
	}, 5)
	if res := c.Check(t.Context(), m); res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
	if gotHost != "custom.example" {
		t.Errorf("server saw Host = %q, want custom.example", gotHost)
	}
}

func TestHTTPHidesURLSecretOnConnectionClose(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Close() // close before ever responding
	}()

	c := newTestChecker()
	url := "http://" + ln.Addr().String() + "/?api_key=SECRET"
	m := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: url}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown {
		t.Fatalf("Status = %v, Message = %q, want Down", res.Status, res.Message)
	}
	if strings.Contains(res.Message, "SECRET") {
		t.Errorf("Message = %q, leaked the query secret", res.Message)
	}
	if strings.Contains(res.Message, url) || strings.Contains(res.Message, ln.Addr().String()) {
		t.Errorf("Message = %q, leaked the full request URL", res.Message)
	}
}

// expiredCert generates a self-signed, already-expired TLS certificate for
// 127.0.0.1 and returns it along with its NotAfter time.
func expiredCert(t *testing.T) (tls.Certificate, time.Time) {
	t.Helper()
	notAfter := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "upsera-test"},
		NotBefore:    notAfter.Add(-48 * time.Hour),
		NotAfter:     notAfter,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return cert, notAfter
}
