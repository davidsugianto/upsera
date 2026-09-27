package notify

import (
	"fmt"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// Title is the one-line summary of ev.
func Title(ev Event) string {
	switch ev.Kind {
	case model.EventDown:
		return "[DOWN] " + ev.MonitorName
	case model.EventRecovered:
		return "[UP] " + ev.MonitorName
	case model.EventFlapping:
		return "[FLAPPING] " + ev.MonitorName
	case model.EventCertExpiry:
		return "[CERT] " + ev.MonitorName
	case model.EventTest:
		return "[TEST] Upsera"
	}
	return fmt.Sprintf("[%s] %s", ev.Kind, ev.MonitorName)
}

// Body is the plain-text detail of ev.
func Body(ev Event) string {
	switch ev.Kind {
	case model.EventDown:
		return "Reason: " + ev.Message + "\nSince: " + ev.At.UTC().Format(time.RFC1123)
	case model.EventRecovered:
		return "Recovered after " + ev.Downtime.Round(time.Second).String()
	case model.EventFlapping:
		return fmt.Sprintf("State changed %d times in the last hour; alerts are held back until it settles.", ev.FlapCount)
	case model.EventCertExpiry:
		return fmt.Sprintf("TLS certificate expires in %d day(s) (%s).", ev.CertDaysLeft, ev.CertExpiresAt.UTC().Format("2006-01-02"))
	case model.EventTest:
		return `Test notification from Upsera channel "` + ev.ChannelName + `".`
	}
	return ev.Message
}
