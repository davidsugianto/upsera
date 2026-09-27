// Package testutil provides a real Postgres for integration tests.
package testutil

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/davidsugianto/upsera/internal/store"
)

// PostgresURL starts a disposable Postgres 17 container and returns its URL.
// It skips the test when UPSERA_SKIP_INTEGRATION=1 or -short is set, and
// fails when Docker is unavailable, so CI never silently skips.
func PostgresURL(t testing.TB) string {
	t.Helper()
	if testing.Short() || os.Getenv("UPSERA_SKIP_INTEGRATION") == "1" {
		t.Skip("integration test: needs Docker")
	}
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("upsera"),
		tcpostgres.WithUsername("upsera"),
		tcpostgres.WithPassword("upsera"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(c); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	return url
}

// Store starts Postgres, applies migrations and returns an open store.
func Store(t testing.TB) (*store.Store, string) {
	t.Helper()
	url := PostgresURL(t)
	ctx := context.Background()
	st, err := store.Open(ctx, url, 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st, url
}
