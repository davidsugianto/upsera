package notify

import (
	"fmt"
	"strconv"
	"strings"
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
	case model.EventProbeSilent:
		return "[PROBE SILENT] " + ev.Region
	case model.EventProbeRecovered:
		return "[PROBE UP] " + ev.Region
	case model.EventDegraded:
		return "[DEGRADED] " + ev.MonitorName
	case model.EventLatencyRecovered:
		return "[LATENCY OK] " + ev.MonitorName
	case model.EventSLOBurn:
		return fmt.Sprintf("[SLO BURN] %s (%s)", ev.MonitorName, ev.SLOLevel)
	case model.EventSLOBurnRecovered:
		return fmt.Sprintf("[SLO RECOVERED] %s (%s)", ev.MonitorName, ev.SLOLevel)
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
	case model.EventProbeSilent:
		return fmt.Sprintf("No contact from probe region %q since %s.\nMonitors checked from this region show a probe-silent warning, not DOWN; the other regions keep checking.",
			ev.Region, ev.At.UTC().Format(time.RFC1123))
	case model.EventProbeRecovered:
		return fmt.Sprintf("Probe region %q is reporting again (%s).", ev.Region, ev.At.UTC().Format(time.RFC1123))
	case model.EventDegraded:
		return fmt.Sprintf("Slow responses%s: p95 %d ms over the last 15 minutes, against a 7-day baseline of %d ms.\nThe monitor is still up; no alert is opened.",
			inRegion(ev.Region), ev.LatencyP95Ms, ev.BaselineMs)
	case model.EventLatencyRecovered:
		if ev.Region == "" {
			return "Response times are back to normal."
		}
		return fmt.Sprintf("Response times%s are back to normal: p95 %d ms (baseline %d ms).",
			inRegion(ev.Region), ev.LatencyP95Ms, ev.BaselineMs)
	case model.EventSLOBurn:
		return fmt.Sprintf("%s burn: the error budget of the %s%% SLO is burning %.1f× faster than sustainable.\n%s\nNo alert is opened.",
			levelName(ev.SLOLevel), formatTarget(ev.SLOTarget), ev.BurnRate, consumedText(ev.BudgetConsumed))
	case model.EventSLOBurnRecovered:
		return fmt.Sprintf("%s burn has stopped: the error budget of the %s%% SLO now burns at %.1f×.\n%s",
			levelName(ev.SLOLevel), formatTarget(ev.SLOTarget), ev.BurnRate, consumedText(ev.BudgetConsumed))
	case model.EventTest:
		return `Test notification from Upsera channel "` + ev.ChannelName + `".`
	}
	return ev.Message
}

// levelName capitalises a burn level ("fast" -> "Fast").
func levelName(level string) string {
	if level == "" {
		return "SLO"
	}
	return strings.ToUpper(level[:1]) + level[1:]
}

// formatTarget prints a target percent without trailing zeros (99.9, 99.95).
func formatTarget(t float64) string { return strconv.FormatFloat(t, 'f', -1, 64) }

func consumedText(c float64) string {
	if c < 0 {
		return "Budget consumed over the SLO window: unknown."
	}
	return fmt.Sprintf("Budget consumed over the SLO window: %.0f%%.", c*100)
}

func inRegion(region string) string {
	if region == "" {
		return ""
	}
	return fmt.Sprintf(" in region %q", region)
}
