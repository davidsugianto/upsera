package statuspage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func mailTestPage() model.StatusPage {
	return model.StatusPage{ID: 7, Slug: "acme", Title: "Acme Status"}
}

func mailTestIncident() model.Incident {
	resolvedAt := testNow.Add(time.Hour)
	return model.Incident{
		ID: 11, PageID: 7, Title: "API outage", Severity: model.SeverityMajor,
		Status: model.IncidentResolved, StartedAt: testNow, ResolvedAt: &resolvedAt,
		Updates: []model.IncidentUpdate{
			{ID: 1, Status: model.IncidentInvestigating, Body: "We are looking into it.", CreatedAt: testNow},
			{ID: 2, Status: model.IncidentResolved, Body: "All good again.", CreatedAt: resolvedAt},
		},
	}
}

func TestMailConfirmMessage(t *testing.T) {
	s := newTestService(t, &fakeStore{}, nil, nil)
	subject, body := s.confirmMessage(mailTestPage(), "https://upsera.test/status/acme/confirm?t=abc")
	if subject != "Confirm your subscription to Acme Status" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"https://upsera.test/status/acme/confirm?t=abc", "ignore"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestMailIncidentMessage(t *testing.T) {
	s := newTestService(t, &fakeStore{}, nil, func(o *Options) {
		o.Loc = time.FixedZone("WIB", 7*3600)
	})
	page := mailTestPage()
	const unsub = "https://upsera.test/status/acme/unsubscribe?t=xyz"

	t.Run("resolved with announced update", func(t *testing.T) {
		in := mailTestIncident()
		subject, body := s.incidentMessage(page, in, new(int64(2)), unsub)
		if subject != "[Acme Status] Resolved: API outage" {
			t.Errorf("subject = %q", subject)
		}
		for _, want := range []string{
			"API outage", "major", "resolved", "All good again.",
			"https://upsera.test/status/acme", unsub, "20:00 WIB", // testNow+1h = 13:00 UTC
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, "We are looking into it.") {
			t.Errorf("body has the other update:\n%s", body)
		}
	})

	t.Run("open incident announces a specific earlier update", func(t *testing.T) {
		in := mailTestIncident()
		in.Status = model.IncidentInvestigating
		in.ResolvedAt = nil
		subject, body := s.incidentMessage(page, in, new(int64(1)), unsub)
		if subject != "[Acme Status] API outage" {
			t.Errorf("subject = %q", subject)
		}
		if !strings.Contains(body, "We are looking into it.") || strings.Contains(body, "All good again.") {
			t.Errorf("wrong update in body:\n%s", body)
		}
	})

	t.Run("nil update id uses the latest update", func(t *testing.T) {
		_, body := s.incidentMessage(page, mailTestIncident(), nil, unsub)
		if !strings.Contains(body, "All good again.") {
			t.Errorf("latest update missing:\n%s", body)
		}
	})

	t.Run("unknown update id falls back to the latest", func(t *testing.T) {
		_, body := s.incidentMessage(page, mailTestIncident(), new(int64(99)), unsub)
		if !strings.Contains(body, "All good again.") {
			t.Errorf("latest update missing:\n%s", body)
		}
	})

	t.Run("no updates", func(t *testing.T) {
		in := mailTestIncident()
		in.Updates = nil
		in.Status = model.IncidentInvestigating
		subject, body := s.incidentMessage(page, in, nil, unsub)
		if subject != "[Acme Status] API outage" {
			t.Errorf("subject = %q", subject)
		}
		for _, want := range []string{"API outage", "investigating", unsub} {
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q:\n%s", want, body)
			}
		}
	})

	t.Run("subject has no CR or LF", func(t *testing.T) {
		in := mailTestIncident()
		in.Title = "Bad\r\nBcc: x@evil.test"
		subject, _ := s.incidentMessage(page, in, nil, unsub)
		if strings.ContainsAny(subject, "\r\n") {
			t.Errorf("subject has line breaks: %q", subject)
		}
	})
}

func TestMailWebhookPayload(t *testing.T) {
	s := newTestService(t, &fakeStore{}, nil, nil)
	page := mailTestPage()
	in := mailTestIncident()

	b, err := json.Marshal(s.webhookPayload(page, in, new(int64(2))))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "incident" {
		t.Errorf("event = %v", got["event"])
	}
	pg := got["page"].(map[string]any)
	if pg["title"] != "Acme Status" || pg["url"] != "https://upsera.test/status/acme" {
		t.Errorf("page = %v", pg)
	}
	inc := got["incident"].(map[string]any)
	if inc["id"] != float64(11) || inc["title"] != "API outage" || inc["severity"] != "major" ||
		inc["status"] != "resolved" || inc["started_at"] != testNow.Format(time.RFC3339) ||
		inc["resolved_at"] != testNow.Add(time.Hour).Format(time.RFC3339) {
		t.Errorf("incident = %v", inc)
	}
	up := got["update"].(map[string]any)
	if up["status"] != "resolved" || up["body"] != "All good again." ||
		up["created_at"] != testNow.Add(time.Hour).Format(time.RFC3339) {
		t.Errorf("update = %v", up)
	}

	t.Run("no updates and unresolved", func(t *testing.T) {
		in := mailTestIncident()
		in.Updates, in.ResolvedAt = nil, nil
		b, _ := json.Marshal(s.webhookPayload(page, in, nil))
		var got map[string]any
		_ = json.Unmarshal(b, &got)
		if v, ok := got["update"]; !ok || v != nil {
			t.Errorf("update = %v (present %v), want null", v, ok)
		}
		if got["incident"].(map[string]any)["resolved_at"] != nil {
			t.Errorf("resolved_at = %v, want null", got["incident"].(map[string]any)["resolved_at"])
		}
	})
}
