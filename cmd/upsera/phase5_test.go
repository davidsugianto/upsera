package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// TestPhase5DoneWhen: "two public pages render with different monitors, and
// a subscriber gets the incident email". The real server serves two pages
// from different monitors, a custom domain, an email subscriber who
// double-opts-in, a published incident that reaches them (and the RSS
// feed), and an outage that is auto-drafted for review and resolved again.
func TestPhase5DoneWhen(t *testing.T) {
	dbURL := testutil.PostgresURL(t)
	smtpAddr, mails := testutil.FakeSMTP(t)
	smtpHost, smtpPortStr, _ := net.SplitHostPort(smtpAddr)
	smtpPort, _ := strconv.Atoi(smtpPortStr)

	base, stop := startServer(t, dbURL, func(c *config.Config) { c.IncidentDraftAfter = time.Second })
	t.Cleanup(func() {
		if t.Failed() {
			dumpDeliveries(t, dbURL)
			for _, m := range mails() {
				t.Logf("mail to %v:\n%s", m.To, m.Data)
			}
		}
	})

	// First-run setup, keeping the admin's browser session for instance
	// settings (a team token cannot reach them).
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	var me struct {
		Teams []struct {
			ID int64 `json:"id"`
		} `json:"teams"`
		CSRFToken string `json:"csrf_token"`
	}
	call(t, browser, "POST", base+"/api/setup", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	}, http.StatusCreated, &me)
	teamURL := fmt.Sprintf("%s/api/teams/%d", base, me.Teams[0].ID)
	var tok struct {
		Token string `json:"token"`
	}
	callCSRF(t, browser, "POST", teamURL+"/tokens", me.CSRFToken, map[string]any{"name": "ci", "scope": "write"}, &tok)
	bearer := "Bearer " + tok.Token

	// Instance SMTP for subscriber email.
	doCall(t, browser, "PUT", base+"/api/admin/settings", map[string]string{"X-CSRF-Token": me.CSRFToken}, map[string]any{
		"block_private_targets": false,
		"smtp":                  map[string]any{"host": smtpHost, "port": smtpPort, "from": "status@example.com", "tls": "none"},
	}, http.StatusOK, nil)

	type idResp struct {
		ID      int64  `json:"id"`
		PushURL string `json:"push_url"`
	}
	pushMonitor := func(name string) idResp {
		var m idResp
		call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, map[string]any{
			"name": name, "type": "push", "interval_s": 3600, "retries": 0, "config": map[string]any{},
		}, http.StatusCreated, &m)
		return m
	}
	alpha := pushMonitor("alpha-internal-db-7")
	beta := pushMonitor("beta-internal-queue-3")
	for _, m := range []idResp{alpha, beta} {
		call(t, http.DefaultClient, "GET", m.PushURL+"?status=up", "", nil, http.StatusOK, nil)
	}

	type pageResp struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	newPage := func(slug, title, public string, m idResp, published bool, domain *string) pageResp {
		body := map[string]any{
			"slug": slug, "title": title, "published": published, "theme": "auto",
			"items": []map[string]any{{"section": "Core", "monitor_id": m.ID, "display_name": public}},
		}
		if domain != nil {
			body["custom_domain"] = *domain
		}
		var p pageResp
		if domain != nil {
			// Custom domains are instance-admin only: use the admin's browser
			// session; a team token is refused.
			call(t, http.DefaultClient, "POST", teamURL+"/status-pages", bearer, body, http.StatusForbidden, nil)
			doCall(t, browser, "POST", teamURL+"/status-pages", map[string]string{"X-CSRF-Token": me.CSRFToken}, body, http.StatusCreated, &p)
			return p
		}
		call(t, http.DefaultClient, "POST", teamURL+"/status-pages", bearer, body, http.StatusCreated, &p)
		return p
	}
	domain := "status.acme.test"
	acme := newPage("acme", "Acme Status", "Public API", alpha, true, &domain)
	newPage("betaco", "Beta Co Status", "Billing", beta, true, nil)
	newPage("hidden", "Hidden", "Secret", beta, false, nil)

	get := func(url, host string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", url, nil)
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Two public pages, each with only its own monitors, and no internals.
	code, acmeHTML := get(base+"/status/acme", "")
	if code != http.StatusOK || !strings.Contains(acmeHTML, "Acme Status") || !strings.Contains(acmeHTML, "Public API") ||
		strings.Contains(acmeHTML, "Billing") || strings.Contains(acmeHTML, "alpha-internal") {
		t.Fatalf("acme page: %d\n%s", code, acmeHTML)
	}
	code, betaHTML := get(base+"/status/betaco", "")
	if code != http.StatusOK || !strings.Contains(betaHTML, "Billing") || strings.Contains(betaHTML, "Public API") ||
		strings.Contains(betaHTML, "beta-internal") {
		t.Fatalf("beta page: %d\n%s", code, betaHTML)
	}
	if code, _ := get(base+"/status/hidden", ""); code != http.StatusNotFound {
		t.Fatalf("unpublished page: %d, want 404", code)
	}
	if code, _ := get(base+"/status/nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown page: %d, want 404", code)
	}

	// Custom domain: served by Host, gated for TLS, API not exposed there.
	if code, body := get(base+"/", domain); code != http.StatusOK || !strings.Contains(body, "Acme Status") {
		t.Fatalf("custom domain root: %d\n%s", code, body)
	}
	if code, _ := get(base+"/api/auth/me", domain); code != http.StatusNotFound {
		t.Fatalf("API on a custom domain: %d, want 404", code)
	}
	if code, _ := get(base+"/api/tls-allowed?domain="+domain, ""); code != http.StatusOK {
		t.Fatalf("tls-allowed for the page's domain: %d", code)
	}
	if code, _ := get(base+"/api/tls-allowed?domain=evil.example.com", ""); code != http.StatusNotFound {
		t.Fatalf("tls-allowed for an unknown domain: %d, want 404", code)
	}

	// Email subscriber: double opt-in.
	postForm := func(u, host string, v url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", u, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := postForm(base+"/status/acme/subscribe", "", url.Values{"email": {"ops@example.com"}}); code != http.StatusOK {
		t.Fatalf("subscribe: %d %s", code, body)
	}
	// The page has a custom domain, so its email links use it (https, root
	// paths); the test reaches the same server with that Host header.
	linkRe := func(path string) *regexp.Regexp {
		return regexp.MustCompile(`https://` + regexp.QuoteMeta(domain) + `/` + path + `\?t=[A-Za-z0-9_-]+`)
	}
	local := func(link string) string { return base + strings.TrimPrefix(link, "https://"+domain) }
	var confirmLink string
	waitFor(t, 30*time.Second, func() bool {
		for _, m := range mails() {
			if strings.Contains(m.Data, "Confirm your subscription") {
				confirmLink = linkRe("confirm").FindString(m.Data)
				return confirmLink != ""
			}
		}
		return false
	})
	var subs struct {
		Subscribers []struct {
			ID        int64  `json:"id"`
			Kind      string `json:"kind"`
			Confirmed bool   `json:"confirmed"`
		} `json:"subscribers"`
	}
	listSubs := func() {
		call(t, http.DefaultClient, "GET", fmt.Sprintf("%s/status-pages/%d/subscribers", teamURL, acme.ID), bearer, nil, http.StatusOK, &subs)
	}
	listSubs()
	if len(subs.Subscribers) != 1 || subs.Subscribers[0].Confirmed {
		t.Fatalf("subscriber before confirming: %+v", subs)
	}
	if code, _ := get(local(confirmLink), domain); code != http.StatusOK { // GET only shows the button
		t.Fatalf("confirm page: %d", code)
	}
	listSubs()
	if subs.Subscribers[0].Confirmed {
		t.Fatal("GET on the confirm link must not confirm")
	}
	if code, body := postForm(local(confirmLink), domain, nil); code != http.StatusOK {
		t.Fatalf("confirm: %d %s", code, body)
	}
	listSubs()
	if !subs.Subscribers[0].Confirmed {
		t.Fatalf("subscriber after confirming: %+v", subs)
	}

	// A published incident reaches the subscriber and the feed.
	var inc struct {
		ID int64 `json:"id"`
	}
	call(t, http.DefaultClient, "POST", fmt.Sprintf("%s/status-pages/%d/incidents", teamURL, acme.ID), bearer, map[string]any{
		"title": "Elevated API errors", "severity": "minor", "status": "investigating", "body": "We are looking into it.",
	}, http.StatusCreated, &inc)
	var incidentMail string
	waitFor(t, 30*time.Second, func() bool {
		for _, m := range mails() {
			if strings.Contains(m.Data, "Elevated API errors") && len(m.To) == 1 && m.To[0] == "ops@example.com" {
				incidentMail = m.Data
				return true
			}
		}
		return false
	})
	unsubLink := linkRe("unsubscribe").FindString(incidentMail)
	if unsubLink == "" || !strings.Contains(incidentMail, "We are looking into it.") {
		t.Fatalf("incident mail lacks the update or the unsubscribe link:\n%s", incidentMail)
	}
	if _, body := get(base+"/status/acme", ""); !strings.Contains(body, "Elevated API errors") {
		t.Fatalf("published incident missing from the page (cache must be invalidated):\n%s", body)
	}
	if _, feed := get(base+"/status/acme/feed.xml", ""); !strings.Contains(feed, "Elevated API errors") {
		t.Fatalf("feed:\n%s", feed)
	}

	// Auto-draft: an outage on a listed monitor becomes a private draft.
	call(t, http.DefaultClient, "GET", alpha.PushURL+"?status=down&msg=boom", "", nil, http.StatusOK, nil)
	type incList struct {
		Incidents []struct {
			ID     int64  `json:"id"`
			Title  string `json:"title"`
			Draft  bool   `json:"draft"`
			Auto   bool   `json:"auto"`
			Status string `json:"status"`
		} `json:"incidents"`
	}
	var il incList
	incidentsURL := fmt.Sprintf("%s/status-pages/%d/incidents", teamURL, acme.ID)
	waitFor(t, 40*time.Second, func() bool {
		call(t, http.DefaultClient, "GET", incidentsURL, bearer, nil, http.StatusOK, &il)
		for _, i := range il.Incidents {
			if i.Auto && i.Draft {
				return true
			}
		}
		return false
	})
	if _, body := get(base+"/status/acme", ""); strings.Contains(body, "service disruption") {
		t.Fatalf("a draft incident must not be public:\n%s", body)
	}
	call(t, http.DefaultClient, "GET", alpha.PushURL+"?status=up", "", nil, http.StatusOK, nil)
	waitFor(t, 40*time.Second, func() bool {
		call(t, http.DefaultClient, "GET", incidentsURL, bearer, nil, http.StatusOK, &il)
		for _, i := range il.Incidents {
			if i.Auto && i.Status == "resolved" {
				return true
			}
		}
		return false
	})

	// Unsubscribe removes the subscriber.
	if code, body := postForm(local(unsubLink), domain, nil); code != http.StatusOK {
		t.Fatalf("unsubscribe: %d %s", code, body)
	}
	listSubs()
	if len(subs.Subscribers) != 0 {
		t.Fatalf("subscribers after unsubscribing: %+v", subs)
	}

	if err := stop(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// dumpDeliveries logs the subscriber outbox, to see why a mail never came.
func dumpDeliveries(t *testing.T, dbURL string) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Logf("dump: %v", err)
		return
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, "SELECT id, kind, state, attempts, last_error, next_attempt_at, now() FROM upsera.subscriber_deliveries ORDER BY id")
	if err != nil {
		t.Logf("dump: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var kind, state, lastErr string
		var attempts int
		var next, now time.Time
		if err := rows.Scan(&id, &kind, &state, &attempts, &lastErr, &next, &now); err == nil {
			t.Logf("delivery %d %s state=%s attempts=%d err=%q next=%s db_now=%s", id, kind, state, attempts, lastErr, next.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		}
	}
}
