// Package telemetry sets up the process's logs, traces, and metrics:
//
//   - logs: log/slog JSON on stdout (the OTel Collector ships pod logs), with trace and span IDs;
//   - traces: the OTel SDK exporting OTLP/gRPC to the collector (OTEL_EXPORTER_OTLP_* configure it);
//   - metrics: one Prometheus registry served on the ops port. OTel instruments from libraries
//     (otelpgx pool stats, otelriver) are bridged into the same registry.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gracefulinfra/relay-api/internal/buildinfo"
)

// Telemetry holds what main needs to serve metrics and shut down cleanly.
type Telemetry struct {
	Registry *prometheus.Registry
	shutdown []func(context.Context) error
}

// Options configure Setup.
type Options struct {
	Service        string // relay-api, relay-worker, relay-migrate
	TracesExporter string // "otlp" or "none"
}

// Setup installs the global tracer and meter providers and the text-map propagator.
func Setup(ctx context.Context, o Options) (*Telemetry, error) {
	info := buildinfo.Get(o.Service)
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(o.Service),
		semconv.ServiceVersion(info.Version),
		semconv.ServiceNamespace("relay"),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	t := &Telemetry{Registry: prometheus.NewRegistry()}
	t.Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if o.TracesExporter == "otlp" {
		exp, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("telemetry: otlp exporter: %w", err)
		}
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.shutdown = append(t.shutdown, tp.Shutdown)

	promExp, err := otelprom.New(otelprom.WithRegisterer(t.Registry), otelprom.WithoutScopeInfo())
	if err != nil {
		return nil, fmt.Errorf("telemetry: prometheus bridge: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(promExp))
	otel.SetMeterProvider(mp)
	t.shutdown = append(t.shutdown, mp.Shutdown)
	return t, nil
}

// Shutdown flushes pending spans.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, f := range t.shutdown {
		errs = append(errs, f(ctx))
	}
	return errors.Join(errs...)
}

// NewLogger returns a JSON logger that adds trace_id and span_id from the context.
func NewLogger(w io.Writer, level slog.Level, service string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(traceHandler{h}).With(slog.String("service", service))
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() && !hasAttr(r, "trace_id") {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}

func (h traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(as)}
}
func (h traceHandler) WithGroup(n string) slog.Handler { return traceHandler{h.Handler.WithGroup(n)} }
