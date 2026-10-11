package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestProbes(t *testing.T) {
	st, url := testutil.Store(t)
	ctx := context.Background()
	_, team, err := st.Setup(ctx, "a@example.com", "A", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}

	probes, err := st.ListProbes(ctx)
	if err != nil || len(probes) != 1 || probes[0].Region != "local" || !probes[0].Registered {
		t.Fatalf("fresh install must list only the registered local probe: %+v %v", probes, err)
	}
	local := probes[0]
	if auth, err := st.ListProbeAuth(ctx); err != nil || len(auth) != 0 {
		t.Fatalf("ListProbeAuth must exclude local: %+v %v", auth, err)
	}
	regions, err := st.ProbeRegions(ctx)
	if err != nil || regions[local.ID] != "local" || len(regions) != 1 {
		t.Fatalf("ProbeRegions: %+v %v", regions, err)
	}

	p, token, err := st.CreateProbe(ctx, "Singapore", "sg-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Registered || p.RegistrationExpiresAt == nil || p.Region != "sg-1" || p.Name != "Singapore" {
		t.Fatalf("created probe: %+v", p)
	}
	if !strings.HasPrefix(token, "upr_") || len(token) != len("upr_")+43 {
		t.Fatalf("registration token shape: %q", token)
	}
	if d := time.Until(*p.RegistrationExpiresAt); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("registration should expire in ~24h, got %v", d)
	}

	t.Run("duplicate name or region conflicts", func(t *testing.T) {
		if _, _, err := st.CreateProbe(ctx, "Singapore", "other"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup name: %v", err)
		}
		if _, _, err := st.CreateProbe(ctx, "Other", "sg-1"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup region: %v", err)
		}
		if _, _, err := st.CreateProbe(ctx, "Another local", "local"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("local region: %v", err)
		}
	})

	t.Run("unregistered probes are not authenticated and not secrets in db", func(t *testing.T) {
		auth, err := st.ListProbeAuth(ctx)
		if err != nil || len(auth) != 0 {
			t.Fatalf("unregistered must not be in ListProbeAuth: %+v %v", auth, err)
		}
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var hash []byte
		if err := conn.QueryRow(ctx, "SELECT registration_token_hash FROM upsera.probes WHERE id = $1", p.ID).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256([]byte(token))
		if string(hash) != string(want[:]) {
			t.Fatal("registration token must be stored as its sha256")
		}
	})

	t.Run("redeem is single use and exchanges for a credential", func(t *testing.T) {
		if _, _, _, err := st.RedeemRegistration(ctx, "upr_nope", "v1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown token: %v", err)
		}
		got, cred, auth, err := st.RedeemRegistration(ctx, token, "1.2.3")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(cred, "upp_") || len(cred) != len("upp_")+43 {
			t.Fatalf("credential shape: %q", cred)
		}
		sum := sha256.Sum256([]byte(cred))
		if got.ID != p.ID || !got.Registered || got.RegistrationExpiresAt != nil || got.Version != "1.2.3" ||
			auth.ID != p.ID || auth.Region != "sg-1" || string(auth.TokenHash) != string(sum[:]) {
			t.Fatalf("redeemed: %+v auth=%+v", got, auth)
		}
		if _, _, _, err := st.RedeemRegistration(ctx, token, "1.2.3"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("second redeem must fail: %v", err)
		}
		auths, err := st.ListProbeAuth(ctx)
		if err != nil || len(auths) != 1 || auths[0].ID != p.ID || string(auths[0].TokenHash) != string(sum[:]) {
			t.Fatalf("ListProbeAuth: %+v %v", auths, err)
		}
	})

	t.Run("issue revokes credential and old token is unusable", func(t *testing.T) {
		tok, exp, err := st.IssueRegistrationToken(ctx, p.ID, time.Hour)
		if err != nil || !strings.HasPrefix(tok, "upr_") || time.Until(exp) > 61*time.Minute || time.Until(exp) < 59*time.Minute {
			t.Fatalf("issue: %q %v %v", tok, exp, err)
		}
		if auths, _ := st.ListProbeAuth(ctx); len(auths) != 0 {
			t.Fatalf("credential must be revoked: %+v", auths)
		}
		ps, _ := st.ListProbes(ctx)
		for _, x := range ps {
			if x.ID == p.ID && (x.Registered || x.RegistrationExpiresAt == nil) {
				t.Fatalf("probe should be unregistered with an expiry: %+v", x)
			}
		}
		if _, _, err := st.IssueRegistrationToken(ctx, local.ID, time.Hour); !errors.Is(err, store.ErrLocalProbe) {
			t.Fatalf("local: %v", err)
		}
		if _, _, err := st.IssueRegistrationToken(ctx, 99999, time.Hour); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
		if _, _, _, err := st.RedeemRegistration(ctx, tok, ""); err != nil {
			t.Fatalf("redeem reissued: %v", err)
		}
	})

	t.Run("expired token cannot be redeemed", func(t *testing.T) {
		q, tok, err := st.CreateProbe(ctx, "Exp", "exp-1")
		if err != nil {
			t.Fatal(err)
		}
		tok2, _, err := st.IssueRegistrationToken(ctx, q.ID, -time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range []string{tok, tok2} {
			if _, _, _, err := st.RedeemRegistration(ctx, x, ""); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("expired/replaced token %q: %v", x, err)
			}
		}
		if err := st.DeleteProbe(ctx, q.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("touch is monotonic", func(t *testing.T) {
		t1 := time.Now().UTC().Truncate(time.Millisecond)
		t0 := t1.Add(-time.Hour)
		if err := st.TouchProbes(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if err := st.TouchProbes(ctx, []model.ProbeSeen{{ID: p.ID, At: t1, Version: "2.0.0"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.TouchProbes(ctx, []model.ProbeSeen{{ID: p.ID, At: t0, Version: "1.9.0"}, {ID: 99999, At: t0}}); err != nil {
			t.Fatal(err)
		}
		ps, _ := st.ListProbes(ctx)
		for _, x := range ps {
			if x.ID == p.ID {
				if x.LastSeenAt == nil || !x.LastSeenAt.Equal(t1) {
					t.Fatalf("last_seen moved backwards: %v want %v", x.LastSeenAt, t1)
				}
				if x.Version == "" {
					t.Fatal("version should be set")
				}
			}
		}
		if auths, _ := st.ListProbeAuth(ctx); len(auths) != 1 || auths[0].LastSeenAt == nil || !auths[0].LastSeenAt.Equal(t1) {
			t.Fatalf("ListProbeAuth last seen: %+v", auths)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := st.DeleteProbe(ctx, local.ID); !errors.Is(err, store.ErrLocalProbe) {
			t.Fatalf("local: %v", err)
		}
		if err := st.DeleteProbe(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: "web", Type: model.TypeHTTP,
			Config: json.RawMessage(`{}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10,
			Regions: []string{"local", "sg-1"}, Quorum: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteProbe(ctx, p.ID); !errors.Is(err, store.ErrInUse) {
			t.Fatalf("in use: %v", err)
		}
		if err := st.DeleteMonitor(ctx, team.ID, m.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteProbe(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ProbeRegions(ctx); err != nil {
			t.Fatal(err)
		}
		if ps, _ := st.ListProbes(ctx); len(ps) != 1 {
			t.Fatalf("only local should remain: %+v", ps)
		}
	})
}
