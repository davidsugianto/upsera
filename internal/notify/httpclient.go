package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/davidsugianto/upsera/internal/netpolicy"
)

const maxResponseBytes = 64 << 10

// newHTTPClient returns a client that dials through p (when non-nil) and
// never through a proxy, times out after 10s and never follows redirects (a redirect could
// bounce a notification to a target the policy would refuse by name).
func newHTTPClient(p *netpolicy.Policy) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// No HTTP(S)_PROXY: a proxy would dial the target on our behalf and
	// bypass the outbound policy, which only sees the proxy's address.
	tr.Proxy = nil
	if p != nil {
		tr.DialContext = p.DialContext
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// describe strips the request URL from a client error: *url.Error's text
// includes the full URL, which holds secrets (bot tokens, webhook paths).
func describe(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return errors.New(ue.Err.Error())
	}
	return err
}

// postJSON POSTs body as JSON and returns at most 64 KiB of the response.
// A non-2xx status is an error. Errors never include the URL.
func postJSON(ctx context.Context, c *http.Client, u string, headers map[string]string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("invalid request URL")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "Upsera")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doRequest(c, req)
}

func doRequest(c *http.Client, req *http.Request) ([]byte, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, describe(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, describe(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return data, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return data, nil
}
