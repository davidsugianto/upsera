package store_test

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/davidsugianto/upsera/internal/testutil"
)

func newProvider(t *testing.T, db *sql.DB, fsys fstest.MapFS) *goose.Provider {
	t.Helper()
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys,
		goose.WithTableName("upsera.goose_db_version"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func eventCheck(t *testing.T, db *sql.DB) string {
	t.Helper()
	var def string
	err := db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'notification_log_event_check'`).Scan(&def)
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var reg sql.NullString
	if err := db.QueryRow(`SELECT to_regclass($1)::text`, name).Scan(&reg); err != nil {
		t.Fatal(err)
	}
	return reg.Valid
}

// Migration 00006 (SLOs, wider notification_log event check) can be rolled
// back and re-applied, and a binary that only knows migrations 1-5 (the
// rollback path in README.md) starts against a database already at version 6.
func TestMigration00006RollbackAndOlderBinary(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("pgx", testutil.PostgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS upsera"); err != nil {
		t.Fatal(err)
	}

	// The test is about migration 6, so it only loads migrations 1-6 and
	// stays valid when later migrations are added.
	all := fstest.MapFS{}
	older := fstest.MapFS{}
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			t.Fatalf("migration %q has no numeric prefix: %v", e.Name(), err)
		}
		if n > 6 {
			continue
		}
		b, err := os.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		all[e.Name()] = &fstest.MapFile{Data: b}
		if n < 6 {
			older[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if len(older) != len(all)-1 {
		t.Fatalf("expected exactly one 00006 migration, have %d of %d files", len(all)-len(older), len(all))
	}

	p := newProvider(t, db, all)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !tableExists(t, db, "upsera.slos") || !strings.Contains(eventCheck(t, db), "slo_burn_recovered") {
		t.Fatal("after up: slos table or widened event check missing")
	}

	// Down removes the SLO table and restores the old check.
	if res, err := p.Down(ctx); err != nil || res.Source.Version != 6 {
		t.Fatalf("down: %+v, %v", res, err)
	}
	if tableExists(t, db, "upsera.slos") || strings.Contains(eventCheck(t, db), "degraded") {
		t.Fatalf("after down: slos still there or check still wide: %s", eventCheck(t, db))
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if !tableExists(t, db, "upsera.slos") {
		t.Fatal("after re-up: slos table missing")
	}

	// The manual rollback documented in README.md: the Down statements of
	// the file plus deleting the version row, with no goose tooling. The
	// migration must then apply cleanly again.
	file, err := os.ReadFile("migrations/00006_slos.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, downSQL, ok := strings.Cut(string(file), "-- +goose Down")
	if !ok {
		t.Fatal("00006_slos.sql has no -- +goose Down section")
	}
	if _, err := db.ExecContext(ctx, downSQL); err != nil {
		t.Fatalf("manual down statements: %v", err)
	}
	del, err := db.ExecContext(ctx, `DELETE FROM upsera.goose_db_version WHERE version_id = 6`)
	if err != nil {
		t.Fatalf("documented DELETE: %v", err)
	}
	if n, _ := del.RowsAffected(); n != 1 {
		t.Fatalf("documented DELETE removed %d rows, want 1", n)
	}
	if tableExists(t, db, "upsera.slos") || strings.Contains(eventCheck(t, db), "degraded") {
		t.Fatal("after the manual rollback: slos still there or check still wide")
	}
	if applied, err := p.Up(ctx); err != nil || len(applied) != 1 || applied[0].Source.Version != 6 {
		t.Fatalf("up after the manual rollback: %+v, %v", applied, err)
	}
	if !tableExists(t, db, "upsera.slos") || !strings.Contains(eventCheck(t, db), "slo_burn_recovered") {
		t.Fatal("after re-applying 00006: slos table or widened event check missing")
	}

	// A binary that only has migrations 1-5 against the database at 6.
	old := newProvider(t, db, older)
	res, err := old.Up(ctx)
	if err != nil {
		t.Fatalf("older binary Up on a database at version 6: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("older binary applied %d migrations, want none", len(res))
	}
}
