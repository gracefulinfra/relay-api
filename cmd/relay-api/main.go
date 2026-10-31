// Command relay-api serves the Relay HTTP API (/v0) and the ops endpoints (/healthz, /readyz,
// /metrics) on a separate port.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/gracefulinfra/relay-api/api"
	"github.com/gracefulinfra/relay-api/internal/platform/access"
	"github.com/gracefulinfra/relay-api/internal/platform/app"
	"github.com/gracefulinfra/relay-api/internal/platform/health"
	"github.com/gracefulinfra/relay-api/internal/platform/idempotency"
	"github.com/gracefulinfra/relay-api/internal/platform/server"
	"github.com/gracefulinfra/relay-api/internal/platform/staffauth"
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
	if len(a.Args) > 0 {
		return subcommand(ctx, a, os.Stdout)
	}

	issuer, jwksURL, audience, err := c.StaffAuth()
	if err != nil {
		return err
	}
	authn, err := staffauth.NewAuthenticator(ctx, staffauth.OIDCConfig{Issuer: issuer, JWKSURL: jwksURL, Audience: audience}, a.Pool, a.Logger)
	if err != nil {
		return err
	}
	policy, err := staffauth.PolicyFromContract()
	if err != nil {
		return err
	}
	authz := &staffauth.Authorizer{Policy: policy, Pool: a.Pool, Log: a.Logger}

	checks, err := a.ReadinessChecks()
	if err != nil {
		return err
	}
	handler := server.New(&api.Server{Access: &access.Service{Pool: a.Pool}}, server.Options{
		Pool:          a.Pool,
		Logger:        a.Logger,
		Registerer:    a.Telemetry.Registry,
		Authenticator: authn,
		Authorizer:    authz,
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

// subcommand runs the one-shot administrative commands: `relay-api bootstrap-admin --subject <sub>`.
func subcommand(ctx context.Context, a *app.App, out *os.File) error {
	if a.Args[0] != "bootstrap-admin" {
		return fmt.Errorf("unknown command %q (known: bootstrap-admin)", a.Args[0])
	}
	fs := flag.NewFlagSet("bootstrap-admin", flag.ContinueOnError)
	subject := fs.String("subject", "", "the person's OIDC subject (sub claim); they must have signed in once")
	issuer := fs.String("issuer", a.Config.OIDCIssuer, "the OIDC issuer (default RELAY_OIDC_ISSUER)")
	if err := fs.Parse(a.Args[1:]); err != nil {
		return err
	}
	if *subject == "" || *issuer == "" {
		return errors.New("bootstrap-admin needs --subject and an issuer (--issuer or RELAY_OIDC_ISSUER)")
	}
	id, err := access.Bootstrap(ctx, a.Pool, *issuer, *subject)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "staff user %s is now the network admin; bootstrap-admin is disabled\n", id)
	return err
}
