// Command relay-api serves the Relay HTTP API (/v0) and the ops endpoints (/healthz, /readyz,
// /metrics) on a separate port.
package main

import (
	"context"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/gracefulinfra/relay-api/api"
	"github.com/gracefulinfra/relay-api/internal/platform/app"
	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	"github.com/gracefulinfra/relay-api/internal/platform/health"
	"github.com/gracefulinfra/relay-api/internal/platform/idempotency"
	"github.com/gracefulinfra/relay-api/internal/platform/server"
)

const name = "relay-api"

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
	handler := server.New(&api.Server{}, server.Options{
		Pool:       a.Pool,
		Logger:     a.Logger,
		Registerer: a.Telemetry.Registry,
		// P1-02 replaces these with Keycloak OIDC and show-scoped RBAC.
		Authenticator: auth.DenyAll{},
		Authorizer:    auth.DenyAll{},
		Idempotency: idempotency.Options{
			TTL: c.IdempotencyTTL, LockTimeout: c.IdempotencyLockTimeout, MaxBody: c.MaxRequestBody, Logger: a.Logger,
		},
		CORSOrigins: c.CORSAllowedOrigins,
	})

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return app.Serve(ctx, app.Server(c.HTTPAddr, handler, a.Logger), c.ShutdownTimeout)
	})
	g.Go(func() error {
		return app.Serve(ctx, app.Server(c.OpsAddr, health.Handler(a.Telemetry.Registry, checks...), a.Logger), c.ShutdownTimeout)
	})
	return g.Wait()
}
