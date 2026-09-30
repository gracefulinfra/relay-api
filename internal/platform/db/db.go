// Package db owns the PostgreSQL connection pool and the request-scoped transaction.
//
// A request that mutates state runs in one transaction that the idempotency middleware opens. Module
// code gets it with Querier, so a state change, its audit record, its River job, and the stored
// idempotent response commit or roll back together (conventions: transaction-aware interfaces).
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options configure Open.
type Options struct {
	URL            string
	MaxConns       int32
	ConnectTimeout time.Duration
	// AppName is reported as application_name, so pg_stat_activity shows which binary holds a connection.
	AppName string
}

// Open creates a traced pool and checks that the database answers.
func Open(ctx context.Context, o Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(o.URL)
	if err != nil {
		// pgx errors can echo the connection string; never wrap them.
		return nil, errors.New("db: invalid RELAY_DATABASE_URL")
	}
	cfg.MaxConns = o.MaxConns
	cfg.ConnConfig.ConnectTimeout = o.ConnectTimeout
	cfg.ConnConfig.RuntimeParams["application_name"] = o.AppName
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}
	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: pool metrics: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, o.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// Queryer is what sqlc-generated queries run on: a pool, a connection, or a transaction.
type Queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

// WithTx stores the request transaction in ctx.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFrom returns the request transaction, if there is one.
func TxFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

// Querier returns the request transaction when there is one, otherwise the pool. Module code must use
// it for every write, and must never commit or roll back the request transaction itself.
func Querier(ctx context.Context, pool *pgxpool.Pool) Queryer {
	if tx, ok := TxFrom(ctx); ok {
		return tx
	}
	return pool
}
