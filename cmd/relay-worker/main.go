// Command relay-worker runs River job workers (same image as relay-api, different entrypoint) and
// serves the ops endpoints.
package main

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/gracefulinfra/relay-api/internal/platform/app"
	"github.com/gracefulinfra/relay-api/internal/platform/health"
	"github.com/gracefulinfra/relay-api/internal/platform/jobs"
)

const name = "relay-worker"

// maxWorkers keeps the laptop profile small; P1-05 makes queues and concurrency configurable.
const maxWorkers = 2

func main() { app.Main(name, run) }

func run(ctx context.Context, args []string) error {
	a, done, err := app.Start(ctx, name, args, os.Stdout)
	if done || err != nil {
		return err
	}
	defer a.Close()
	c := a.Config

	checks, err := a.ReadinessChecks()
	if err != nil {
		return err
	}
	client, err := jobs.NewClient(a.Pool, jobs.Options{Logger: a.Logger, Registerer: a.Telemetry.Registry, MaxWorkers: maxWorkers})
	if err != nil {
		return err
	}
	checks = append(checks, health.Check{Name: "river", Fn: func(context.Context) error {
		select {
		case <-client.Stopped():
			return errors.New("river client stopped")
		default:
			return nil
		}
	}})

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		// Start returns once the client runs; it stops when ctx ends, finishing running jobs first.
		if err := client.Start(ctx); err != nil {
			return err
		}
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
		defer cancel()
		return client.Stop(sctx)
	})
	g.Go(func() error {
		return app.Serve(ctx, app.Server(c.OpsAddr, health.Handler(a.Telemetry.Registry, checks...), a.Logger), c.ShutdownTimeout)
	})
	return g.Wait()
}
