// Package httpx holds the global HTTP middleware: panic recovery, JSON access logs, CORS, and request
// metrics. The per-operation middleware (authorization, idempotency) runs after routing; see server.
package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gracefulinfra/relay-api/internal/platform/problem"
	"github.com/gracefulinfra/relay-api/internal/platform/requestid"
)

// Route carries the matched route pattern from the router back out to the global middleware, which
// run before routing and see a different *http.Request.
type Route struct{ Pattern string }

type routeKey struct{}

func routeFrom(ctx context.Context) *Route {
	rt, _ := ctx.Value(routeKey{}).(*Route)
	return rt
}

// RecordRoute is a per-operation middleware: it records r.Pattern for logs and metrics, and names the
// trace span after it.
func RecordRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt := routeFrom(r.Context()); rt != nil {
			rt.Pattern = r.Pattern
		}
		span := trace.SpanFromContext(r.Context())
		span.SetName(r.Pattern)
		span.SetAttributes(attribute.String("http.route", r.Pattern))
		next.ServeHTTP(w, r)
	})
}

// statusWriter records the status and size of a response.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Metrics are the HTTP server's Prometheus collectors.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers the collectors on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_http_requests_total",
			Help: "HTTP requests by method, route pattern, and status code.",
		}, []string{"method", "route", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "relay_http_request_duration_seconds",
			Help:    "HTTP request duration by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// Observe is the outermost middleware after the request ID: it recovers panics as problem+json, writes
// one JSON access-log line per request, and records metrics. It never logs headers, the query string
// (it can carry tokens in later slices), or bodies.
func Observe(log *slog.Logger, m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rt := &Route{}
			sw := &statusWriter{ResponseWriter: w}
			ctx := context.WithValue(r.Context(), routeKey{}, rt)
			r = r.WithContext(ctx)

			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
						panic(v)
					}
					log.ErrorContext(ctx, "panic", slog.Any("panic", v), slog.String("stack", string(debug.Stack())))
					if sw.status == 0 {
						problem.Write(sw, r, problem.Internal())
					}
				}
				route := rt.Pattern
				if route == "" {
					route = "unmatched"
				} else if _, p, ok := strings.Cut(route, " "); ok {
					route = p
				}
				status := sw.status
				if status == 0 {
					status = http.StatusOK
				}
				d := time.Since(start)
				m.requests.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()
				m.duration.WithLabelValues(r.Method, route).Observe(d.Seconds())

				attrs := []slog.Attr{
					slog.String("request_id", requestid.From(ctx)),
					slog.String("method", r.Method),
					slog.String("path", r.URL.EscapedPath()),
					slog.String("route", route),
					slog.Int("status", status),
					slog.Int("bytes", sw.bytes),
					slog.Float64("duration_ms", float64(d.Microseconds())/1000),
				}
				if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
					attrs = append(attrs, slog.String("trace_id", sc.TraceID().String()))
				}
				log.LogAttrs(ctx, slog.LevelInfo, "http request", attrs...)
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

// CORS allows the listed browser origins (the admin app) and nothing else. Credentials are bearer
// tokens in the Authorization header, so cookies are never allowed.
func CORS(allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			w.Header().Add("Vary", "Origin")
			if origin == "" || !slices.Contains(allowed, origin) {
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					problem.Write(w, r, problem.Forbidden())
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", "ETag, Location, X-Request-Id, Idempotent-Replayed, Retry-After")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, If-Match, X-Request-Id")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
