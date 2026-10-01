// Package app is the startup and shutdown shared by relay-api, relay-worker, and relay-migrate: flags,
// config, logging, telemetry, the database pool, and the ops server.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gracefulinfra/relay-api/internal/buildinfo"
	"github.com/gracefulinfra/relay-api/internal/platform/config"
	"github.com/gracefulinfra/relay-api/internal/platform/db"
	"github.com/gracefulinfra/relay-api/internal/platform/health"
	"github.com/gracefulinfra/relay-api/internal/platform/telemetry"
)

// App is a started process.
type App struct {
	Name      string
	Config    config.Config
	Logger    *slog.Logger
	Telemetry *telemetry.Telemetry
	Pool      *pgxpool.Pool
	// Args are the positional arguments left after flags.
	Args []string
}

// Start handles the common flags (--print-config, --version), then loads config, sets up telemetry,
// and opens the pool. done=true means a flag was handled and main should exit 0. args excludes the
// program name; flag.ContinueOnError returns usage errors instead of exiting.
func Start(ctx context.Context, name string, args []string, stdout io.Writer) (a *App, done bool, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	printConfig := fs.Bool("print-config", false, "print the effective configuration with secrets redacted, then exit")
	version := fs.Bool("version", false, "print build information, then exit")
	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}
	if *version {
		return nil, true, json.NewEncoder(stdout).Encode(buildinfo.Get(name))
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, false, err
	}
	if *printConfig {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return nil, true, enc.Encode(cfg.Redacted())
	}

	log := telemetry.NewLogger(os.Stdout, cfg.LogLevel, name)
	slog.SetDefault(log)
	tel, err := telemetry.Setup(ctx, telemetry.Options{Service: name, TracesExporter: cfg.TracesExporter})
	if err != nil {
		return nil, false, err
	}
	pool, err := db.Open(ctx, db.Options{URL: cfg.DatabaseURL, MaxConns: cfg.DBMaxConns, ConnectTimeout: cfg.DBConnectTimeout, AppName: name})
	if err != nil {
		_ = tel.Shutdown(ctx)
		return nil, false, err
	}
	info := buildinfo.Get(name)
	log.InfoContext(ctx, "starting", slog.String("version", info.Version), slog.String("commit", info.Commit))
	return &App{Name: name, Config: cfg, Logger: log, Telemetry: tel, Pool: pool, Args: fs.Args()}, false, nil
}

// Close releases the pool and flushes telemetry.
func (a *App) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.Pool.Close()
	if err := a.Telemetry.Shutdown(ctx); err != nil {
		a.Logger.Warn("telemetry shutdown", slog.Any("error", err))
	}
}

// ReadinessChecks are the checks every long-running binary shares: the database answers, and it has
// every migration this binary embeds.
func (a *App) ReadinessChecks() ([]health.Check, error) {
	m, err := db.NewMigrator(a.Pool)
	if err != nil {
		return nil, err
	}
	return []health.Check{
		{Name: "database", Fn: a.Pool.Ping},
		{Name: "migrations", Fn: func(ctx context.Context) error {
			ok, err := m.Current(ctx)
			if err != nil {
				return errors.New("cannot read migration state")
			}
			if !ok {
				return errors.New("pending migrations")
			}
			return nil
		}},
	}, nil
}

// Server is an HTTP server with sane timeouts.
func Server(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// Serve runs srv until ctx is done, then shuts it down within timeout.
func Serve(ctx context.Context, srv *http.Server, timeout time.Duration) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// SignalContext is cancelled on SIGINT or SIGTERM (the kubelet's stop signal).
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Main runs fn and exits non-zero on error. It is the whole of each cmd's main.
func Main(name string, fn func(ctx context.Context, args []string) error) {
	ctx, cancel := SignalContext()
	err := fn(ctx, os.Args[1:])
	cancel()
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}
