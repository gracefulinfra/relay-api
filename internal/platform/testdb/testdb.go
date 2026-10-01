// Package testdb runs integration tests against a real PostgreSQL in Docker (testcontainers-go).
//
// Each test package that calls Main gets one container. The migrations are applied once to a template
// database, and each test gets its own database copied from it (CREATE DATABASE ... TEMPLATE), so
// tests are isolated and fast. `go test -short` skips every test that needs the database.
package testdb

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/gracefulinfra/relay-api/internal/platform/db"
)

// Image is the PostgreSQL the platform runs (relay-infra: the CNPG operand and the dev stack use 17.11).
const Image = "docker.io/library/postgres:17.11-trixie@sha256:d74eeac9a635390a49bc21bd49fccd973de707e2a53a76ac49b552b8712ec46f"

const template = "relay_template"

var (
	adminURL string // superuser URL for the postgres database
	counter  atomic.Int64
)

// Main is TestMain for packages with database tests. With -short it runs the tests without starting
// a container; the database tests then skip themselves.
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	flag.Parse() // testing.Short needs parsed flags, and TestMain runs before m.Run parses them
	if testing.Short() || os.Getenv("RELAY_TEST_SKIP_DB") == "1" {
		return m.Run()
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("testdb: start postgres: %v", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			log.Printf("testdb: terminate: %v", err)
		}
	}()
	adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Printf("testdb: connection string: %v", err)
		return 1
	}
	if err := buildTemplate(ctx); err != nil {
		log.Printf("testdb: template: %v", err)
		return 1
	}
	return m.Run()
}

func buildTemplate(ctx context.Context) error {
	if err := exec(ctx, "CREATE DATABASE "+template); err != nil {
		return err
	}
	pool, err := open(ctx, template)
	if err != nil {
		return err
	}
	defer pool.Close()
	mig, err := db.NewMigrator(pool)
	if err != nil {
		return err
	}
	defer func() { _ = mig.Close() }()
	_, err = mig.Up(ctx)
	return err
}

// URL returns a connection URL for database name.
func URL(name string) string {
	u, _ := url.Parse(adminURL)
	u.Path = "/" + name
	return u.String()
}

func open(ctx context.Context, name string) (*pgxpool.Pool, error) {
	return db.Open(ctx, db.Options{URL: URL(name), MaxConns: 20, ConnectTimeout: 5 * time.Second, AppName: "relay-test"})
}

func exec(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

// New returns a pool on a fresh, fully migrated database. It skips the test under -short.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	return create(t, " TEMPLATE "+template)
}

// NewEmpty returns a pool on a fresh database with no migrations applied.
func NewEmpty(t testing.TB) *pgxpool.Pool {
	t.Helper()
	return create(t, "")
}

// NewName is like New, and also returns the database name (for building URLs).
func NewName(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	p := New(t)
	return p, p.Config().ConnConfig.Database
}

func create(t testing.TB, suffix string) *pgxpool.Pool {
	t.Helper()
	if adminURL == "" {
		t.Skip("database tests need Docker (skipped with -short or RELAY_TEST_SKIP_DB=1)")
	}
	ctx := context.Background()
	name := fmt.Sprintf("t%d_%s", counter.Add(1), sanitize(t.Name()))
	if err := exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+suffix); err != nil {
		t.Fatalf("testdb: create database: %v", err)
	}
	pool, err := open(ctx, name)
	if err != nil {
		t.Fatalf("testdb: open: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_ = exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})
	return pool
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	b := []byte(s)
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			b[i] = '_'
		}
	}
	if len(b) > 40 {
		b = b[:40]
	}
	return string(b)
}
