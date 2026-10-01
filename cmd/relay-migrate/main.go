// Command relay-migrate applies the embedded goose migrations. It runs as the Argo CD Sync hook Job
// before the API and worker Deployments roll (relay-infra apps/relay-api), and locally from make dev.
//
//	relay-migrate [up]    apply pending migrations (the default)
//	relay-migrate status  list migrations and whether each is applied
//
// There is no down: migrations only move forward (conventions).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/gracefulinfra/relay-api/internal/platform/app"
	"github.com/gracefulinfra/relay-api/internal/platform/db"
)

const name = "relay-migrate"

func main() { app.Main(name, run) }

func run(ctx context.Context, args []string) error {
	a, done, err := app.Start(ctx, name, args, os.Stdout)
	if done || err != nil {
		return err
	}
	defer a.Close()

	m, err := db.NewMigrator(a.Pool)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	cmd := "up"
	if len(a.Args) > 0 {
		cmd = a.Args[0]
	}
	switch cmd {
	case "up":
		results, err := m.Up(ctx)
		for _, r := range results {
			a.Logger.InfoContext(ctx, "migration applied", slog.Int64("version", r.Source.Version),
				slog.String("file", r.Source.Path), slog.Duration("duration", r.Duration))
		}
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		cur, target, err := m.Versions(ctx)
		if err != nil {
			return err
		}
		a.Logger.InfoContext(ctx, "migrations current", slog.Int64("version", cur), slog.Int64("binary_version", target), slog.Int("applied", len(results)))
		return nil
	case "status":
		st, err := m.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range st {
			fmt.Printf("%05d  %-8s  %s\n", s.Source.Version, s.State, s.Source.Path)
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q (want up or status)", cmd)
	}
}
