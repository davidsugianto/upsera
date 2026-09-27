package alerting

import (
	"fmt"
	"math"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// certThresholds are the days-left marks at which a certificate warning is
// sent, smallest first.
var certThresholds = []int{1, 7, 14}

// certScan warns once per (monitor, certificate, threshold) when an
// HTTP/keyword monitor's TLS certificate expires within 14, 7 or 1 days.
func (e *Engine) certScan(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range e.monitors {
		if (m.Type != model.TypeHTTP && m.Type != model.TypeKeyword) || m.Paused {
			continue
		}
		st, ok := e.stateLocked(m.ID)
		if !ok || st.TLSExpiresAt == nil {
			continue
		}
		exp := *st.TLSExpiresAt
		days := int(math.Floor(exp.Sub(now).Hours() / 24))
		threshold := 0
		for _, t := range certThresholds {
			if days <= t {
				threshold = t
				break
			}
		}
		if threshold == 0 {
			continue
		}
		key := fmt.Sprintf("cert:%d:%d:%d", m.ID, exp.Unix(), threshold)
		if _, sent := e.certSent[key]; sent {
			continue
		}
		e.certSent[key] = struct{}{}
		ev := e.eventLocked(model.EventCertExpiry, m)
		ev.At, ev.CertExpiresAt, ev.CertDaysLeft = now, exp, days
		e.sendStepLocked(m, 0, ev, key)
	}
}
