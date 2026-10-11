package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// metricsHandler serves GET /metrics in the Prometheus text format for the
// monitors of the team a bearer API token belongs to (any scope; the read
// scope is enough). Sessions are not accepted: scrapers use tokens.
func metricsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		var tok *model.APIToken
		if strings.HasPrefix(auth, bearerPrefix) {
			var err error
			if tok, err = lookupBearer(r.Context(), d, auth); err != nil {
				d.Logger.Error("metrics: token lookup failed", "error", err)
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if tok == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="upsera"`)
			http.Error(w, "a team API token is required", http.StatusUnauthorized)
			return
		}

		monitors, err := d.Store.ListMonitors(r.Context(), tok.TeamID)
		if err != nil {
			d.Logger.Error("metrics: list monitors failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		beats, err := d.Store.ListRecentHeartbeats(r.Context(), tok.TeamID, 1)
		if err != nil {
			d.Logger.Error("metrics: list heartbeats failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		now := time.Now()
		var up, status, latency, paused, cert metricFamily
		up.head("upsera_monitor_up", "1 when the monitor is up or pending, 0 when down (absent while paused or unchecked).")
		status.head("upsera_monitor_status", "Monitor state: 0 down, 1 up, 2 pending, 3 maintenance (absent while paused or unchecked).")
		latency.head("upsera_monitor_response_ms", "Latency of the most recent check in milliseconds.")
		paused.head("upsera_monitor_paused", "1 when the monitor is paused.")
		cert.head("upsera_monitor_cert_expiry_seconds", "Seconds until the TLS certificate expires (negative once expired).")

		for _, m := range monitors {
			labels := fmt.Sprintf(`monitor_id="%d",monitor="%s",type="%s",group="%s"`,
				m.ID, escapeLabel(m.Name), escapeLabel(string(m.Type)), escapeLabel(m.GroupName))
			paused.sample(labels, boolGauge(m.Paused))
			if hbs := beats[m.ID]; len(hbs) > 0 {
				latency.sample(labels, strconv.FormatInt(int64(hbs[0].LatencyMs), 10))
			}
			if m.Paused {
				continue
			}
			if st, ok := d.Runner.State(m.ID); ok {
				up.sample(labels, boolGauge(st.Status != model.StatusDown))
				status.sample(labels, strconv.Itoa(int(st.Status)))
				if st.TLSExpiresAt != nil {
					cert.sample(labels, strconv.FormatFloat(st.TLSExpiresAt.Sub(now).Seconds(), 'f', 0, 64))
				}
			}
		}

		var buf bytes.Buffer
		for _, f := range []*metricFamily{&up, &status, &latency, &paused, &cert} {
			f.writeTo(&buf)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(buf.Bytes())
	}
}

// metricFamily accumulates one gauge's HELP/TYPE header and samples.
type metricFamily struct {
	name, help string
	samples    []string
}

func (f *metricFamily) head(name, help string) { f.name, f.help = name, help }

func (f *metricFamily) sample(labels, value string) {
	f.samples = append(f.samples, f.name+"{"+labels+"} "+value+"\n")
}

func (f *metricFamily) writeTo(b *bytes.Buffer) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", f.name, f.help, f.name)
	for _, s := range f.samples {
		b.WriteString(s)
	}
}

func boolGauge(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// escapeLabel escapes a label value for the Prometheus text format.
func escapeLabel(s string) string { return labelEscaper.Replace(s) }
