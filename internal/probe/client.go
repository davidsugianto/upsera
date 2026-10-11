package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/davidsugianto/upsera/internal/probeproto"
)

const (
	credentialFile  = "credential"
	maxResponseBody = 8 << 20
)

// errRejected marks a results batch the server refuses for good (retrying
// the same bytes cannot help).
var errRejected = errors.New("server rejected the batch")

func (a *Agent) userAgent() string { return "upsera-probe/" + a.cfg.Version }

// ensureCredential returns the stored credential or registers with the
// configured token and stores the result. The credential and token are never
// logged.
func (a *Agent) ensureCredential(ctx context.Context) (string, error) {
	path := filepath.Join(a.cfg.DataDir, credentialFile)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if cred := strings.TrimSpace(string(data)); cred != "" {
			a.log.Info("using stored probe credential", "path", path)
			return cred, nil
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", &FatalError{Msg: fmt.Sprintf("cannot read credential file %s: %v", path, err)}
	}

	if a.cfg.Token == "" {
		return "", &FatalError{Msg: fmt.Sprintf("no credential in %s and PROBE_TOKEN is not set: create the probe in the Upsera admin UI and start it once with its registration token", a.cfg.DataDir)}
	}
	resp, err := a.register(ctx)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, []byte(resp.Credential+"\n"), 0o600); err != nil {
		return "", &FatalError{Msg: fmt.Sprintf("registered, but cannot store the credential in %s: %v (the registration token is now used up; issue a new one)", a.cfg.DataDir, err)}
	}
	a.log.Info("registered with server", "probe_id", resp.ProbeID, "region", resp.Region)
	return resp.Credential, nil
}

// register exchanges the registration token for a credential. Network errors,
// 429 and 5xx are retried with backoff; any other failure is fatal.
func (a *Agent) register(ctx context.Context) (probeproto.RegisterResponse, error) {
	body, _ := json.Marshal(probeproto.RegisterRequest{Token: a.cfg.Token, Version: a.cfg.Version})
	backoff := a.backoffBase
	for {
		resp, status, err := a.postJSON(ctx, probeproto.PathRegister, "", body)
		if err == nil && (status == http.StatusOK || status == http.StatusCreated) {
			var out probeproto.RegisterResponse
			if derr := json.Unmarshal(resp, &out); derr != nil || strings.TrimSpace(out.Credential) == "" {
				return out, &FatalError{Msg: "registration succeeded but the server's answer carried no credential"}
			}
			out.Credential = strings.TrimSpace(out.Credential)
			return out, nil
		}
		if err == nil && status != http.StatusTooManyRequests && status < 500 {
			return probeproto.RegisterResponse{}, &FatalError{Msg: fmt.Sprintf("registration refused (HTTP %d): the registration token is wrong, expired or already used; issue a new one in the Upsera admin UI", status)}
		}
		if err != nil && ctx.Err() != nil {
			return probeproto.RegisterResponse{}, ctx.Err()
		}
		a.log.Warn("registration failed, will retry", "err", errOrStatus(err, status), "retry_in", backoff)
		if !sleep(ctx, backoff) {
			return probeproto.RegisterResponse{}, ctx.Err()
		}
		backoff = min(backoff*2, a.backoffMax)
	}
}

func errOrStatus(err error, status int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("HTTP %d", status)
}

// postJSON POSTs body and returns the (size-limited) response body and status.
func (a *Agent) postJSON(ctx context.Context, path, bearer string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.userAgent())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return out, resp.StatusCode, nil
}

// unauthorized is the fatal error for a 401 on an authenticated call.
func unauthorized(what string) error {
	return &FatalError{Msg: fmt.Sprintf("server rejected the probe credential (HTTP 401 on %s): the probe was deleted or its credential revoked; delete the credential file in DATA_DIR and restart with a new PROBE_TOKEN", what)}
}

// writeFileAtomic writes data to path via a temp file in the same directory,
// with the given mode from the start, then renames it into place.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
