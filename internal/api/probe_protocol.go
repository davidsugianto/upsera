package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/probeproto"
	"github.com/davidsugianto/upsera/internal/store"
)

// The probe protocol is plain HTTP handlers on the chi router, outside the
// huma session/token middleware: probes authenticate only with an
// "Authorization: Bearer upp_..." credential (never a cookie, so these
// endpoints need no CSRF check, and never a team "ups_" token), and
// registration authenticates with the one-time "upr_..." token in its body.
// Every authentication failure is the same generic 401.

const (
	registerBodyLimit = 8 << 10
	registerMax       = 10 // registrations per IP per registerWindow
	registerWindow    = time.Minute
	registerMaxIPs    = 50000
	maxCredentialLen  = 256 // header length cap before hashing
	maxVersionLen     = 64
	// maxResultAge: results older than this (after the clock shift) are
	// dropped; they predate any retained history and only a broken or hostile
	// probe sends them.
	maxResultAge = 24 * time.Hour
)

// registerLimiter is a fixed-window per-IP counter for the unauthenticated
// registration endpoint. Every attempt counts, successful or not. IPv6
// clients are keyed by their /64.
type registerLimiter struct {
	mu        sync.Mutex
	windows   map[string]*registerWindowCount
	lastSweep time.Time
	now       func() time.Time
}

type registerWindowCount struct {
	start time.Time
	n     int
}

func newRegisterLimiter() *registerLimiter {
	return &registerLimiter{windows: map[string]*registerWindowCount{}, now: time.Now}
}

// limiterKey is the rate-limit key of the client behind r: its IPv4 address,
// or the /64 an IPv6 address belongs to. X-Forwarded-For is believed only when
// the peer is one of trusted (Config.TrustedProxies); then the client is the
// rightmost entry that is not itself a trusted proxy, because leading entries
// are client-supplied. Anything that does not parse falls back to the peer.
func limiterKey(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	ip := peer.Unmap()
	if isTrustedProxy(ip, trusted) {
		if c, ok := forwardedClient(r, trusted); ok {
			ip = c
		}
	}
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}

func isTrustedProxy(ip netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// forwardedClient returns the rightmost X-Forwarded-For address that is not a
// trusted proxy. An entry that is not an IP ends the search without a result.
func forwardedClient(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
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
		if !isTrustedProxy(ip, trusted) {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

// allow records an attempt from key and reports whether it may proceed; when
// not, retryAfter says how long until the window resets.
func (l *registerLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= registerWindow {
		l.lastSweep = now
		for k, w := range l.windows {
			if now.Sub(w.start) >= registerWindow {
				delete(l.windows, k)
			}
		}
	}
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= registerWindow {
		if w == nil && len(l.windows) >= registerMaxIPs {
			return false, registerWindow
		}
		w = &registerWindowCount{start: now}
		l.windows[key] = w
	}
	if w.n >= registerMax {
		return false, w.start.Add(registerWindow).Sub(now)
	}
	w.n++
	return true, 0
}

func writeProbeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProbeErr(w http.ResponseWriter, status int, detail string) {
	writeProbeJSON(w, status, map[string]any{
		"title": http.StatusText(status), "status": status, "detail": detail,
	})
}

// probeUnauthorized is the one response for every bad, missing, expired or
// wrong-kind credential or registration token.
func probeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeProbeErr(w, http.StatusUnauthorized, "unauthorized")
}

// sanitizeProbeVersion makes an agent-supplied version string safe to store
// and show: no control characters, bounded length.
func sanitizeProbeVersion(v string) string {
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, strings.TrimSpace(v))
	if r := []rune(v); len(r) > maxVersionLen {
		v = string(r[:maxVersionLen])
	}
	return v
}

// decodeProbeBody decodes one JSON value from a size-limited body, writing
// the error response itself when it fails.
func decodeProbeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	body := http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(body)
	err := dec.Decode(v)
	if err == nil {
		// Reject trailing data after the document.
		if _, perr := dec.Token(); !errors.Is(perr, io.EOF) {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeProbeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeProbeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// etagMatches reports whether an If-None-Match header lists etag (or "*").
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == "*" || part == etag {
			return true
		}
	}
	return false
}

func registerProbeProtocol(r chi.Router, d Deps) {
	if d.Probes == nil || d.ProbeSink == nil {
		return
	}
	limiter := newRegisterLimiter()

	authenticate := func(req *http.Request) (model.ProbeAuth, bool) {
		h := req.Header.Get("Authorization")
		if len(h) > maxCredentialLen || !strings.HasPrefix(h, bearerPrefix+model.CredentialPrefix) {
			return model.ProbeAuth{}, false
		}
		return d.Probes.Authenticate(strings.TrimPrefix(h, bearerPrefix))
	}

	r.Post(probeproto.PathRegister, func(w http.ResponseWriter, req *http.Request) {
		if ok, retry := limiter.allow(limiterKey(req, d.Config.TrustedProxies)); !ok {
			secs := int(retry/time.Second) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeProbeErr(w, http.StatusTooManyRequests, "too many registration attempts")
			return
		}
		var in probeproto.RegisterRequest
		if !decodeProbeBody(w, req, registerBodyLimit, &in) {
			return
		}
		if len(in.Token) > maxCredentialLen || !strings.HasPrefix(in.Token, model.RegistrationPrefix) {
			probeUnauthorized(w)
			return
		}
		version := sanitizeProbeVersion(in.Version)
		p, credential, auth, err := d.Store.RedeemRegistration(req.Context(), in.Token, version)
		switch {
		case errors.Is(err, store.ErrNotFound):
			probeUnauthorized(w)
			return
		case err != nil:
			d.Logger.Error("probe registration failed", "error", err)
			writeProbeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		d.Probes.Add(auth)
		d.Probes.Seen(auth.ID, version)
		d.Logger.Info("probe registered", "probe_id", p.ID, "region", p.Region)
		writeProbeJSON(w, http.StatusOK, probeproto.RegisterResponse{
			Credential: credential, ProbeID: p.ID, Region: p.Region,
		})
	})

	r.Get(probeproto.PathAssignments, func(w http.ResponseWriter, req *http.Request) {
		auth, ok := authenticate(req)
		if !ok {
			probeUnauthorized(w)
			return
		}
		d.Probes.Seen(auth.ID, "")

		block := d.Policy != nil && d.Policy.BlockPrivate()
		version, ms := d.ProbeSink.Assignments(auth.Region)
		if block {
			// block_private_targets is part of the response, so a change of
			// the setting must change the version (and ETag) too.
			version += "+bp"
		}
		etag := `"` + version + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if etagMatches(req.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		out := probeproto.AssignmentsResponse{
			Version: version, Region: auth.Region, BlockPrivateTargets: block,
			Monitors: make([]probeproto.Assignment, 0, len(ms)),
		}
		for _, m := range ms {
			// The sink already filters; re-check so a sink bug can never hand a
			// probe another region's, paused or non-remote monitor.
			if m.Paused || !m.Type.RemoteCapable() || !m.InRegion(auth.Region) {
				continue
			}
			out.Monitors = append(out.Monitors, probeproto.Assignment{
				MonitorID: m.ID, Name: m.Name, Type: string(m.Type), Config: m.Config,
				IntervalS: m.IntervalS, RetryIntervalS: m.RetryIntervalS, Retries: m.Retries, TimeoutS: m.TimeoutS,
			})
		}
		writeProbeJSON(w, http.StatusOK, out)
	})

	r.Post(probeproto.PathResults, func(w http.ResponseWriter, req *http.Request) {
		auth, ok := authenticate(req)
		if !ok {
			probeUnauthorized(w)
			return
		}
		var in probeproto.ResultsRequest
		version := ""
		defer func() { d.Probes.Seen(auth.ID, version) }()
		if !decodeProbeBody(w, req, probeproto.MaxBatchBytes, &in) {
			return
		}
		version = sanitizeProbeVersion(in.Version)
		if len(in.Results) > probeproto.MaxResultsPerBatch {
			writeProbeErr(w, http.StatusRequestEntityTooLarge, "too many results in one batch")
			return
		}
		if in.SentAt.IsZero() {
			writeProbeErr(w, http.StatusUnprocessableEntity, "sent_at is required")
			return
		}
		if len(in.Results) == 0 {
			writeProbeJSON(w, http.StatusOK, probeproto.ResultsResponse{})
			return
		}

		now := time.Now()
		shift := now.Sub(in.SentAt)  // probe clock -> server clock
		assigned := map[int64]bool{} // per-batch memo of ProbeSink.Assigned

		rs := make([]model.RegionResult, 0, len(in.Results))
		for _, res := range in.Results {
			if res.Time.IsZero() {
				continue
			}
			ok, seen := assigned[res.MonitorID]
			if !seen {
				ok = d.ProbeSink.Assigned(auth.Region, res.MonitorID)
				assigned[res.MonitorID] = ok
			}
			if !ok {
				continue
			}
			t := res.Time.Add(shift)
			if t.After(now) {
				t = now
			}
			if now.Sub(t) > maxResultAge {
				continue
			}
			status := model.StatusDown
			if res.OK {
				status = model.StatusUp
			}
			latency := res.LatencyMs
			if latency < 0 {
				latency = 0
			}
			rs = append(rs, model.RegionResult{
				MonitorID: res.MonitorID, Time: t, Status: status, LatencyMs: latency,
				Message: model.TruncateMessage(res.Message), TLSExpiresAt: res.TLSExpiresAt,
			})
		}
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Time.Before(rs[j].Time) })
		if len(rs) > 0 {
			d.ProbeSink.Report(auth.ID, auth.Region, rs)
		}
		writeProbeJSON(w, http.StatusOK, probeproto.ResultsResponse{Accepted: len(rs)})
	})
}
