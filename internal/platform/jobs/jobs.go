// Package jobs configures River, the durable job queue in the same PostgreSQL database (decision A7).
// Tables live in the `river` schema (migration 00002). relay-worker runs the client; P1-05 adds the
// job kinds and the Argo Workflows bridge, and gives relay-api an insert-only client for
// transactional enqueue.
//
// P1-01 registers one maintenance job, IdempotencyCleanup, which enforces the Idempotency-Key TTL.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"github.com/riverqueue/rivercontrib/otelriver"

	"github.com/gracefulinfra/relay-api/internal/platform/idempotency/idempotencydb"
)

// Schema is where River's tables live.
const Schema = "river"

// Options configure NewClient.
type Options struct {
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	// MaxWorkers bounds concurrent jobs in the default queue. The laptop profile keeps it small.
	MaxWorkers int
}

// NewClient returns a River client that works jobs. Call Start to begin.
func NewClient(pool *pgxpool.Pool, o Options) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	cleanup := &IdempotencyCleanupWorker{
		pool:    pool,
		logger:  o.Logger,
		deleted: prometheus.NewCounter(prometheus.CounterOpts{Name: "relay_idempotency_keys_expired_total", Help: "Expired Idempotency-Key rows deleted by the cleanup job."}),
	}
	o.Registerer.MustRegister(cleanup.deleted)
	if err := river.AddWorkerSafely(workers, cleanup); err != nil {
		return nil, fmt.Errorf("jobs: register worker: %w", err)
	}

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema:     Schema,
		Logger:     o.Logger,
		Queues:     map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: o.MaxWorkers}},
		Workers:    workers,
		Middleware: []rivertype.Middleware{otelriver.NewMiddleware(nil)},
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return IdempotencyCleanupArgs{}, nil },
				&river.PeriodicJobOpts{ID: "idempotency-cleanup", RunOnStart: true}),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: river client: %w", err)
	}
	return client, nil
}

// IdempotencyCleanupArgs is the periodic cleanup of expired Idempotency-Key rows.
type IdempotencyCleanupArgs struct{}

// Kind implements river.JobArgs.
func (IdempotencyCleanupArgs) Kind() string { return "platform.idempotency_cleanup" }

// InsertOpts implements river.JobArgsWithInsertOpts: at most one pending run at a time.
func (IdempotencyCleanupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: 15 * time.Minute}}
}

// IdempotencyCleanupWorker deletes expired rows in batches, each in its own short statement.
type IdempotencyCleanupWorker struct {
	river.WorkerDefaults[IdempotencyCleanupArgs]
	pool    *pgxpool.Pool
	logger  *slog.Logger
	deleted prometheus.Counter
}

const cleanupBatch = 1000

// Work implements river.Worker.
func (w *IdempotencyCleanupWorker) Work(ctx context.Context, _ *river.Job[IdempotencyCleanupArgs]) error {
	q := idempotencydb.New(w.pool)
	var total int64
	for {
		n, err := q.DeleteExpiredIdempotencyKeys(ctx, cleanupBatch)
		if err != nil {
			return fmt.Errorf("delete expired idempotency keys: %w", err)
		}
		total += n
		w.deleted.Add(float64(n))
		if n < cleanupBatch {
			break
		}
	}
	w.logger.InfoContext(ctx, "idempotency cleanup", slog.Int64("deleted", total))
	return nil
}
