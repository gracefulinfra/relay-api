package db_test

import (
	"context"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/gracefulinfra/relay-api/db/migrations"
	"github.com/gracefulinfra/relay-api/internal/platform/db"
	"github.com/gracefulinfra/relay-api/internal/platform/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

var moduleSchemas = []string{"analytics", "billing", "catalog", "community", "distribution", "feeds",
	"media", "platform", "publishing", "river", "sponsorship", "transcripts"}

func TestFirstInstallAppliesEveryMigration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.NewEmpty(t)
	m, err := db.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	if ok, err := m.Current(ctx); err != nil || ok {
		t.Fatalf("empty database: current=%v err=%v, want not current", ok, err)
	}
	res, err := m.Up(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Errorf("applied %d migrations, want 2", len(res))
	}
	if ok, err := m.Current(ctx); err != nil || !ok {
		t.Fatalf("after up: current=%v err=%v", ok, err)
	}

	rows, err := pool.Query(ctx, `SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%' AND nspname NOT IN ('information_schema', 'public') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	if !slices.Equal(got, moduleSchemas) {
		t.Errorf("schemas = %v, want %v", got, moduleSchemas)
	}
	var riverJob bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('river.river_job') IS NOT NULL`).Scan(&riverJob); err != nil || !riverJob {
		t.Errorf("river.river_job missing (err=%v)", err)
	}
}

func TestUpIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t) // already migrated
	m, err := db.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	res, err := m.Up(ctx)
	if err != nil || len(res) != 0 {
		t.Fatalf("second up: %d applied, err=%v; want 0, nil", len(res), err)
	}
}

// A database migrated by a newer release (a rolling upgrade) is still current for this binary:
// migrations are expand/contract, so old replicas stay ready while new ones roll out.
func TestDatabaseAheadOfBinaryIsCurrent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	newer := withExtra(t, "00003_newer.sql", "-- +goose Up\nCREATE TABLE catalog.added_by_newer_release (id int);\n")
	mNew, err := db.NewMigratorFS(pool, newer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mNew.Close() }()
	if _, err := mNew.Up(ctx); err != nil {
		t.Fatal(err)
	}

	mOld, err := db.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mOld.Close() }()
	if ok, err := mOld.Current(ctx); err != nil || !ok {
		t.Fatalf("older binary: current=%v err=%v, want current", ok, err)
	}
}

// An upgrade whose migration fails leaves the database at the last good version, and the running
// release keeps working (its readiness stays green).
func TestFailedUpgradeLeavesDatabaseAtLastGoodVersion(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	bad := withExtra(t, "00003_broken.sql",
		"-- +goose Up\nCREATE TABLE catalog.half_done (id int);\nALTER TABLE catalog.does_not_exist ADD COLUMN x int;\n")
	m, err := db.NewMigratorFS(pool, bad)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err := m.Up(ctx); err == nil {
		t.Fatal("broken migration succeeded")
	}
	cur, _, err := m.Versions(ctx)
	if err != nil || cur != 2 {
		t.Fatalf("version after failure = %d (err %v), want 2", cur, err)
	}
	var half bool
	_ = pool.QueryRow(ctx, `SELECT to_regclass('catalog.half_done') IS NOT NULL`).Scan(&half)
	if half {
		t.Error("a failed migration left a partial change (it must run in one transaction)")
	}
	good, err := db.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = good.Close() }()
	if ok, _ := good.Current(ctx); !ok {
		t.Error("the running release became unready after a failed upgrade")
	}
}

func withExtra(t *testing.T, name, sql string) fs.FS {
	t.Helper()
	out := fstest.MapFS{name: {Data: []byte(sql)}}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = &fstest.MapFile{Data: b}
	}
	return out
}
