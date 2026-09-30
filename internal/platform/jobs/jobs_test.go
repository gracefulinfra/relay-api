package jobs_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gracefulinfra/relay-api/internal/platform/jobs"
	"github.com/gracefulinfra/relay-api/internal/platform/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// The worker's River client starts against the migrated `river` schema and runs the periodic
// idempotency cleanup on start, which deletes only expired keys.
func TestWorkerRunsIdempotencyCleanup(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	_, err := pool.Exec(ctx, `
		INSERT INTO platform.idempotency_keys (actor, method, path, key, request_hash, response_status, response_headers, response_body, created_at, expires_at)
		SELECT 'a', 'POST', '/v0/x', 'k' || g, sha256(g::text::bytea), 201, '{}', '', now() - interval '2 days', now() - interval '1 day'
		FROM generate_series(1, 2500) g;
		INSERT INTO platform.idempotency_keys (actor, method, path, key, request_hash, response_status, response_headers, response_body, expires_at)
		VALUES ('a', 'POST', '/v0/x', 'live', sha256('live'), 201, '{}', '', now() + interval '1 day');`)
	if err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	client, err := jobs.NewClient(pool, jobs.Options{Logger: slog.New(slog.DiscardHandler), Registerer: reg, MaxWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(ctx) }()

	select {
	case ev := <-events:
		if ev.Kind != river.EventKindJobCompleted || ev.Job.Kind != "platform.idempotency_cleanup" || ev.Job.State != rivertype.JobStateCompleted {
			t.Fatalf("event = %s %s %s", ev.Kind, ev.Job.Kind, ev.Job.State)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cleanup job did not run")
	}

	var left []string
	rows, _ := pool.Query(ctx, `SELECT key FROM platform.idempotency_keys`)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		left = append(left, k)
	}
	if len(left) != 1 || left[0] != "live" {
		t.Errorf("remaining keys = %v, want [live]", left)
	}
	if n := testutil.ToFloat64(firstCounter(t, reg)); n != 2500 {
		t.Errorf("relay_idempotency_keys_expired_total = %v, want 2500", n)
	}
}

func firstCounter(t *testing.T, reg *prometheus.Registry) prometheus.Collector {
	t.Helper()
	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() == "relay_idempotency_keys_expired_total" {
			v := mf.GetMetric()[0].GetCounter().GetValue()
			return prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "x"}, func() float64 { return v })
		}
	}
	t.Fatal("counter not registered")
	return nil
}
