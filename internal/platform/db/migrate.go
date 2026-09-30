package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/gracefulinfra/relay-api/db/migrations"
)

// Migrator applies and inspects the embedded goose migrations.
type Migrator struct {
	sqlDB    *sql.DB
	provider *goose.Provider
}

// NewMigrator wraps pool for goose. A Postgres advisory lock serializes concurrent runs, so two
// migration Jobs (a retried sync, or two environments sharing a database by mistake) cannot interleave.
func NewMigrator(pool *pgxpool.Pool) (*Migrator, error) {
	return newMigrator(pool, migrations.FS)
}

func newMigrator(pool *pgxpool.Pool, fsys fs.FS) (*Migrator, error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("migrate: locker: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys,
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate: provider: %w", err)
	}
	return &Migrator{sqlDB: sqlDB, provider: p}, nil
}

// Up applies every pending migration. Each migration runs in its own transaction, so a failure leaves
// the database at the last migration that succeeded.
func (m *Migrator) Up(ctx context.Context) ([]*goose.MigrationResult, error) {
	return m.provider.Up(ctx)
}

// Status lists every embedded migration and whether it is applied.
func (m *Migrator) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	return m.provider.Status(ctx)
}

// Versions returns the database's version and the newest version this binary embeds.
func (m *Migrator) Versions(ctx context.Context) (current, target int64, err error) {
	return m.provider.GetVersions(ctx)
}

// Current reports whether the database has every migration this binary knows. A database that is
// ahead (a newer release migrated it during a rolling upgrade) counts as current: migrations are
// expand/contract, so the older binary keeps working.
func (m *Migrator) Current(ctx context.Context) (bool, error) {
	pending, err := m.provider.HasPending(ctx)
	if err != nil {
		return false, err
	}
	return !pending, nil
}

// Close releases the database/sql wrapper. It does not close the pool.
func (m *Migrator) Close() error {
	return m.sqlDB.Close()
}
