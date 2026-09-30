// Package server assembles the /v0 HTTP handler from the contract-generated router and the platform
// middleware. The order is:
//
//	request ID → OTel tracing → observe (recover, access log, metrics) → CORS → authenticate
//	  → router (relay-contracts gen/go) → per operation: record route → authorize → idempotency → handler
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	relayapi "github.com/gracefulinfra/relay-contracts/gen/go/relayapi"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	"github.com/gracefulinfra/relay-api/internal/platform/httpx"
	"github.com/gracefulinfra/relay-api/internal/platform/idempotency"
	"github.com/gracefulinfra/relay-api/internal/platform/problem"
	"github.com/gracefulinfra/relay-api/internal/platform/requestid"
)

// BaseURL is where the contract's paths are mounted (servers[].url in OpenAPI v0).
const BaseURL = "/v0"

// ErrNotImplemented is returned by operations that are in the contract but not built yet. It maps
// to 501 problem+json.
var ErrNotImplemented = errors.New("not implemented")

// Options assemble a handler.
type Options struct {
	Pool          *pgxpool.Pool
	Logger        *slog.Logger
	Registerer    prometheus.Registerer
	Authenticator auth.Authenticator
	Authorizer    auth.Authorizer
	Idempotency   idempotency.Options
	CORSOrigins   []string
	MaxBody       int64
}

// New returns the API handler for impl.
func New(impl relayapi.StrictServerInterface, o Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, problem.NotFound("No operation matches this method and path."))
	})

	strict := relayapi.NewStrictHandlerWithOptions(impl, nil, relayapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			problem.Write(w, r, problem.BadRequest(err.Error()))
		},
		ResponseErrorHandlerFunc: responseError(o.Logger),
	})
	relayapi.HandlerWithOptions(strict, relayapi.StdHTTPServerOptions{
		BaseURL:    BaseURL,
		BaseRouter: mux,
		// Applied in reverse: the last entry runs first.
		Middlewares: []relayapi.MiddlewareFunc{
			idempotency.Middleware(o.Pool, o.Idempotency),
			auth.Authorize(o.Authorizer),
			httpx.RecordRoute,
		},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			problem.Write(w, r, problem.BadRequest(err.Error()))
		},
	})

	var h http.Handler = mux
	h = auth.Authenticate(o.Authenticator)(h)
	h = httpx.CORS(o.CORSOrigins)(h)
	h = httpx.Observe(o.Logger, httpx.NewMetrics(o.Registerer))(h)
	h = otelhttp.NewHandler(h, "http.server",
		otelhttp.WithMeterProvider(noop.NewMeterProvider()), // httpx.Metrics owns HTTP metrics
		otelhttp.WithFilter(func(r *http.Request) bool { return r.Method != http.MethodOptions }),
	)
	h = requestid.Middleware(h)
	return h
}

func responseError(log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, ErrNotImplemented) {
			problem.Write(w, r, problem.NotImplemented())
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		log.ErrorContext(r.Context(), "handler error", slog.Any("error", err))
		problem.Write(w, r, problem.Internal())
	}
}
