package statuspage

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

func TestHandlerNotFoundIsIdentical(t *testing.T) {
	unpublished := renderTestPage()
	unpublished.Slug = "draft"
	unpublished.Published = false
	st := &fakeStore{GetPublishedPageBySlugFn: func(_ context.Context, slug string) (model.StatusPage, error) {
		switch slug {
		case "acme":
			return renderTestPage(), nil
		case "draft":
			// GetPublishedPageBySlug does not return unpublished pages.
			return model.StatusPage{}, store.ErrNotFound
		case "leaky":
			return unpublished, nil // even if a store returned one, it is not served
		}
		return model.StatusPage{}, store.ErrNotFound
	}}
	svc := newTestService(t, st, fakeStates{}, nil)
	h := svc.Handler()
	for _, suffix := range []string{"", "/feed.xml"} {
		unknown := renderTestGet(h, http.MethodGet, "/status/nope"+suffix)
		for _, slug := range []string{"draft", "leaky"} {
			got := renderTestGet(h, http.MethodGet, "/status/"+slug+suffix)
			if got.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound {
				t.Fatalf("%s%s: %d / unknown %d, want 404s", slug, suffix, got.Code, unknown.Code)
			}
			if got.Body.String() != unknown.Body.String() || got.Header().Get("Content-Type") != unknown.Header().Get("Content-Type") {
				t.Errorf("%s%s differs from unknown slug: %q vs %q", slug, suffix, got.Body, unknown.Body)
			}
		}
	}
}

func TestHandlerNotFoundIsNotCached(t *testing.T) {
	var calls int
	published := false
	st := &fakeStore{GetPublishedPageBySlugFn: func(context.Context, string) (model.StatusPage, error) {
		calls++
		if published {
			return renderTestPage(), nil
		}
		return model.StatusPage{}, store.ErrNotFound
	}}
	svc := newTestService(t, st, fakeStates{}, nil)
	if rec := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	published = true
	if rec := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme"); rec.Code != http.StatusOK || calls != 2 {
		t.Errorf("after publishing: status %d, store calls %d, want 200 and 2", rec.Code, calls)
	}
}

func TestHandlerMethods(t *testing.T) {
	svc := newTestService(t, renderTestStore(renderTestPage()), fakeStates{}, nil)
	h := svc.Handler()
	for _, target := range []string{"/status/acme", "/status/acme/", "/status/acme/feed.xml"} {
		get := renderTestGet(h, http.MethodGet, target)
		head := renderTestGet(h, http.MethodHead, target)
		if get.Code != http.StatusOK || head.Code != http.StatusOK {
			t.Fatalf("%s: GET %d HEAD %d", target, get.Code, head.Code)
		}
		if head.Body.Len() != 0 {
			t.Errorf("%s: HEAD returned a body", target)
		}
		if head.Header().Get("Content-Type") != get.Header().Get("Content-Type") {
			t.Errorf("%s: HEAD and GET content types differ", target)
		}
		if rec := renderTestGet(h, http.MethodDelete, target); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: DELETE = %d, want 405", target, rec.Code)
		}
	}
	if rec := renderTestGet(h, http.MethodGet, "/status"); rec.Code != http.StatusNotFound {
		t.Errorf("/status = %d, want 404", rec.Code)
	}
}

func TestFeed(t *testing.T) {
	const xss = `<b>&"x"</b>`
	page := renderTestPage()
	page.Title = "Acme " + xss
	st := renderTestStore(page)
	pub := testNow.Add(-2 * time.Hour)
	var gotDrafts bool
	var gotLimit int
	st.ListIncidentsFn = func(_ context.Context, pageID int64, drafts bool, limit int) ([]model.Incident, error) {
		gotDrafts, gotLimit = drafts, limit
		return []model.Incident{
			{ID: 11, PageID: pageID, Title: "DB down " + xss, Status: model.IncidentIdentified, StartedAt: pub.Add(-time.Hour), PublishedAt: &pub,
				Updates: []model.IncidentUpdate{
					{Status: model.IncidentInvestigating, Body: "looking"},
					{Status: model.IncidentIdentified, Body: "cause found " + xss},
				}},
			{ID: 12, PageID: pageID, Title: "No updates", Status: model.IncidentInvestigating, StartedAt: pub, PublishedAt: &pub},
			{ID: 13, PageID: pageID, Title: "Hidden draft", Draft: true, StartedAt: pub},
		}, nil
	}
	svc := newTestService(t, st, fakeStates{}, nil)
	rec := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme/feed.xml")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/rss+xml; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if gotDrafts || gotLimit != 20 {
		t.Errorf("ListIncidents(drafts=%v, limit=%d), want false, 20", gotDrafts, gotLimit)
	}

	var feed struct {
		Version string `xml:"version,attr"`
		Channel struct {
			Title string `xml:"title"`
			Link  string `xml:"link"`
			Items []struct {
				Title string `xml:"title"`
				Link  string `xml:"link"`
				GUID  struct {
					Perma string `xml:"isPermaLink,attr"`
					Value string `xml:",chardata"`
				} `xml:"guid"`
				PubDate     string `xml:"pubDate"`
				Description string `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	dec := xml.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	for {
		if _, err := dec.Token(); err != nil {
			if err != io.EOF {
				t.Fatalf("feed is not well-formed XML: %v", err)
			}
			break
		}
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.Version != "2.0" || feed.Channel.Title != page.Title || feed.Channel.Link != "https://upsera.test/status/acme" {
		t.Errorf("channel = %+v", feed.Channel)
	}
	if len(feed.Channel.Items) != 2 {
		t.Fatalf("items = %d, want 2 (drafts excluded)", len(feed.Channel.Items))
	}
	it := feed.Channel.Items[0]
	if it.Title != "DB down "+xss || it.Link != "https://upsera.test/status/acme#incident-11" ||
		it.GUID.Perma != "false" || it.GUID.Value != it.Link {
		t.Errorf("item = %+v", it)
	}
	if want := pub.UTC().Format(time.RFC1123Z); it.PubDate != want {
		t.Errorf("pubDate = %q, want %q", it.PubDate, want)
	}
	if want := "Status: Identified. cause found " + xss; it.Description != want {
		t.Errorf("description = %q, want %q", it.Description, want)
	}
	if d := feed.Channel.Items[1].Description; d != "Status: Investigating" {
		t.Errorf("description without updates = %q", d)
	}
	if strings.Contains(rec.Body.String(), "Hidden draft") {
		t.Error("draft in feed")
	}
}

func TestFeedUsesCustomDomainLinks(t *testing.T) {
	page := renderTestPage()
	page.CustomDomain = new("status.acme.test")
	st := renderTestStore(page)
	st.ListIncidentsFn = func(_ context.Context, pageID int64, _ bool, _ int) ([]model.Incident, error) {
		return []model.Incident{{ID: 1, PageID: pageID, Title: "x", StartedAt: testNow}}, nil
	}
	svc := newTestService(t, st, fakeStates{}, nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme/feed.xml").Body.String()
	if !strings.Contains(body, "<link>https://status.acme.test#incident-1</link>") {
		t.Errorf("feed links do not use the custom domain:\n%s", body)
	}
}
