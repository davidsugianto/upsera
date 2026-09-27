package notify

import (
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestTitleAndBody(t *testing.T) {
	t.Parallel()
	at := time.Date(2024, 3, 10, 7, 30, 0, 0, time.UTC)
	certAt := time.Date(2024, 3, 17, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		ev        Event
		wantTitle string
		wantBody  string
	}{
		{
			ev:        Event{Kind: model.EventDown, MonitorName: "api", Message: "connection refused", At: at},
			wantTitle: "[DOWN] api",
			wantBody:  "Reason: connection refused\nSince: " + at.Format(time.RFC1123),
		},
		{
			ev:        Event{Kind: model.EventRecovered, MonitorName: "api", Downtime: 90*time.Second + 400*time.Millisecond},
			wantTitle: "[UP] api",
			wantBody:  "Recovered after 1m30s",
		},
		{
			ev:        Event{Kind: model.EventFlapping, MonitorName: "api", FlapCount: 7},
			wantTitle: "[FLAPPING] api",
			wantBody:  "State changed 7 times in the last hour; alerts are held back until it settles.",
		},
		{
			ev:        Event{Kind: model.EventCertExpiry, MonitorName: "api", CertDaysLeft: 7, CertExpiresAt: certAt},
			wantTitle: "[CERT] api",
			wantBody:  "TLS certificate expires in 7 day(s) (2024-03-17).",
		},
		{
			ev:        Event{Kind: model.EventTest, ChannelName: "ops-telegram"},
			wantTitle: "[TEST] Upsera",
			wantBody:  `Test notification from Upsera channel "ops-telegram".`,
		},
	}
	for _, c := range cases {
		if got := Title(c.ev); got != c.wantTitle {
			t.Errorf("Title(%s) = %q, want %q", c.ev.Kind, got, c.wantTitle)
		}
		if got := Body(c.ev); got != c.wantBody {
			t.Errorf("Body(%s) = %q, want %q", c.ev.Kind, got, c.wantBody)
		}
	}
}
