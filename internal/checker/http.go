package checker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// maxKeywordBody is the most body a keyword check reads before giving up on
// finding a match.
const maxKeywordBody = 1 << 20 // 1 MiB

func (c *Checker) checkHTTP(ctx context.Context, m model.Monitor, timeoutS int) Result {
	var cfg HTTPConfig
	if err := json.Unmarshal(m.Config, &cfg); err != nil {
		return Result{Status: model.StatusDown, Message: "invalid monitor config"}
	}

	transport := &http.Transport{
		DialContext:       c.policy.DialContext,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: cfg.IgnoreTLSErrors}, //nolint:gosec // opt-in per monitor
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) > cfg.maxRedirects() {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	defer transport.CloseIdleConnections()

	var body io.Reader
	if cfg.Body != "" {
		body = strings.NewReader(cfg.Body)
	}
	req, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, body)
	if err != nil {
		return Result{Status: model.StatusDown, Message: "invalid request: " + err.Error()}
	}
	req.Header.Set("User-Agent", "Upsera/1.0")
	for k, v := range cfg.Headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Result{Status: model.StatusDown, Latency: time.Since(start), Message: describeErr(err, timeoutS)}
	}
	defer resp.Body.Close()

	var tlsExpiresAt *time.Time
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		exp := resp.TLS.PeerCertificates[0].NotAfter
		tlsExpiresAt = &exp
	}

	if !statusAccepted(cfg.AcceptedStatus, resp.StatusCode) {
		return Result{
			Status:       model.StatusDown,
			Latency:      time.Since(start),
			Message:      fmt.Sprintf("HTTP %d", resp.StatusCode),
			TLSExpiresAt: tlsExpiresAt,
		}
	}

	if m.Type != model.TypeKeyword {
		return Result{Status: model.StatusUp, Latency: time.Since(start), TLSExpiresAt: tlsExpiresAt}
	}

	found, err := bodyContainsKeyword(resp.Body, cfg.Keyword, cfg.CaseSensitive)
	if err != nil {
		return Result{Status: model.StatusDown, Latency: time.Since(start), Message: "reading response: " + err.Error(), TLSExpiresAt: tlsExpiresAt}
	}
	up := found != cfg.InvertKeyword
	if up {
		return Result{Status: model.StatusUp, Latency: time.Since(start), TLSExpiresAt: tlsExpiresAt}
	}
	var msg string
	if cfg.InvertKeyword {
		msg = fmt.Sprintf("keyword %q found", cfg.Keyword)
	} else {
		msg = fmt.Sprintf("keyword %q not found", cfg.Keyword)
	}
	return Result{Status: model.StatusDown, Latency: time.Since(start), Message: msg, TLSExpiresAt: tlsExpiresAt}
}

// bodyContainsKeyword reports whether keyword appears in the first
// maxKeywordBody bytes of r.
func bodyContainsKeyword(r io.Reader, keyword string, caseSensitive bool) (bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxKeywordBody))
	if err != nil {
		return false, err
	}
	needle := []byte(keyword)
	if !caseSensitive {
		data = bytes.ToLower(data)
		needle = bytes.ToLower(needle)
	}
	return bytes.Contains(data, needle), nil
}
