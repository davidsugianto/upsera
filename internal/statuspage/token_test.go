package statuspage

import (
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestTokenRoundTrip(t *testing.T) {
	s := newTestService(t, &fakeStore{}, nil, nil)

	id, err := s.parseConfirmToken(s.confirmToken(42))
	if err != nil || id != 42 {
		t.Fatalf("confirm round trip = %d, %v", id, err)
	}
	id, err = s.parseUnsubscribeToken(s.unsubscribeToken(7))
	if err != nil || id != 7 {
		t.Fatalf("unsubscribe round trip = %d, %v", id, err)
	}
	if tok := s.confirmToken(1); strings.ContainsAny(tok, "=+/") {
		t.Fatalf("token %q is not unpadded base64url", tok)
	}
}

func TestTokenPurposeBound(t *testing.T) {
	s := newTestService(t, &fakeStore{}, nil, nil)
	if _, err := s.parseUnsubscribeToken(s.confirmToken(1)); err != errTokenInvalid {
		t.Fatalf("confirm token as unsubscribe: err = %v", err)
	}
	if _, err := s.parseConfirmToken(s.unsubscribeToken(1)); err != errTokenInvalid {
		t.Fatalf("unsubscribe token as confirm: err = %v", err)
	}
}

func TestTokenTamperedAndMalformed(t *testing.T) {
	s := newTestService(t, &fakeStore{}, nil, nil)
	tok := s.confirmToken(5)
	flip := func(i int) string {
		b := []byte(tok)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	cases := map[string]string{
		"empty":         "",
		"garbage":       "not a token!",
		"truncated":     tok[:len(tok)-2],
		"extended":      tok + "AA",
		"flipped id":    flip(3),
		"flipped mac":   flip(len(tok) - 5),
		"too long":      strings.Repeat("A", maxTokenLen+1),
		"standard b64":  strings.NewReplacer("-", "+", "_", "/").Replace(tok) + "==",
		"other secret":  tokenTestOtherSecret(t),
		"unsub as conf": s.unsubscribeToken(5),
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c == tok {
				t.Skip("unchanged")
			}
			if _, err := s.parseConfirmToken(c); err == nil {
				t.Fatalf("token %q verified", c)
			}
		})
	}
}

// tokenTestOtherSecret returns a confirm token minted under a different secret.
func tokenTestOtherSecret(t *testing.T) string {
	t.Helper()
	o := newTestService(t, &fakeStore{}, nil, func(o *Options) { o.Secret = []byte("a-completely-different-secret!!!") })
	return o.confirmToken(5)
}

func TestConfirmTokenExpiry(t *testing.T) {
	now := testNow
	s := newTestService(t, &fakeStore{}, nil, func(o *Options) { o.Now = func() time.Time { return now } })
	tok := s.confirmToken(9)

	now = testNow.Add(confirmTTL)
	if id, err := s.parseConfirmToken(tok); err != nil || id != 9 {
		t.Fatalf("at expiry: %d, %v", id, err)
	}
	now = testNow.Add(confirmTTL + time.Second)
	if _, err := s.parseConfirmToken(tok); err != errTokenExpired {
		t.Fatalf("after expiry: err = %v", err)
	}
}

func TestUnsubscribeTokenNeverExpires(t *testing.T) {
	now := testNow
	s := newTestService(t, &fakeStore{}, nil, func(o *Options) { o.Now = func() time.Time { return now } })
	tok := s.unsubscribeToken(3)
	now = testNow.Add(10 * 365 * 24 * time.Hour)
	if id, err := s.parseUnsubscribeToken(tok); err != nil || id != 3 {
		t.Fatalf("old unsubscribe token: %d, %v", id, err)
	}
}

func TestTokenNoSecretFailsClosed(t *testing.T) {
	s := newTestService(t, &fakeStore{}, nil, func(o *Options) { o.Secret = nil })
	if tok := s.confirmToken(1); tok != "" {
		t.Fatalf("token with no secret = %q", tok)
	}
	if _, err := s.parseConfirmToken("AAAA"); err == nil {
		t.Fatal("verified with no secret")
	}
}

func TestTokenSignedLinks(t *testing.T) {
	s := newTestService(t, &fakeStore{}, nil, nil)
	page := model.StatusPage{ID: 1, Slug: "acme"}
	sub := model.Subscriber{ID: 11, PageID: 1}

	cl := s.confirmLink(page, sub)
	pre := "https://upsera.test/status/acme/confirm?t="
	if !strings.HasPrefix(cl, pre) {
		t.Fatalf("confirm link = %q", cl)
	}
	if id, err := s.parseConfirmToken(strings.TrimPrefix(cl, pre)); err != nil || id != 11 {
		t.Fatalf("confirm link token: %d, %v", id, err)
	}
	ul := s.unsubscribeLink(page, sub)
	pre = "https://upsera.test/status/acme/unsubscribe?t="
	if !strings.HasPrefix(ul, pre) {
		t.Fatalf("unsubscribe link = %q", ul)
	}
	if id, err := s.parseUnsubscribeToken(strings.TrimPrefix(ul, pre)); err != nil || id != 11 {
		t.Fatalf("unsubscribe link token: %d, %v", id, err)
	}

	dom := "status.acme.test"
	page.CustomDomain = &dom
	if got := s.confirmLink(page, sub); !strings.HasPrefix(got, "https://status.acme.test/confirm?t=") {
		t.Fatalf("custom-domain link = %q", got)
	}
}
