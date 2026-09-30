package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relayapi "github.com/gracefulinfra/relay-contracts/gen/go/relayapi"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gracefulinfra/relay-api/api"
	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	"github.com/gracefulinfra/relay-api/internal/platform/idempotency"
	"github.com/gracefulinfra/relay-api/internal/platform/problem"
	"github.com/gracefulinfra/relay-api/internal/platform/requestid"
	"github.com/gracefulinfra/relay-api/internal/platform/server"
	"github.com/gracefulinfra/relay-api/internal/platform/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// panicky overrides one operation to prove panic recovery through the real generated router.
type panicky struct{ api.Server }

func (panicky) GetPublicNetwork(context.Context, relayapi.GetPublicNetworkRequestObject) (relayapi.GetPublicNetworkResponseObject, error) {
	panic("boom")
}

type stack struct {
	h     http.Handler
	reg   *prometheus.Registry
	logs  *bytes.Buffer
	spans *tracetest.SpanRecorder
}

func newStack(t *testing.T, authn auth.Authenticator, authz auth.Authorizer) stack {
	t.Helper()
	s := stack{reg: prometheus.NewRegistry(), logs: &bytes.Buffer{}, spans: tracetest.NewSpanRecorder()}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(s.spans))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	s.h = server.New(panicky{}, server.Options{
		Pool:          testdb.New(t),
		Logger:        slog.New(slog.NewJSONHandler(s.logs, nil)),
		Registerer:    s.reg,
		Authenticator: authn,
		Authorizer:    authz,
		Idempotency:   idempotency.Options{TTL: time.Hour, LockTimeout: time.Second, MaxBody: 1 << 20, Logger: slog.New(slog.DiscardHandler)},
		CORSOrigins:   []string{"https://admin.relay.localtest.me"},
	})
	return s
}

func (s stack) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) problem.Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body %q)", ct, rec.Body.String())
	}
	var p problem.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

type allow struct{}

func (allow) Authenticate(*http.Request) (auth.Actor, bool)             { return auth.Actor{ID: "staff-1"}, true }
func (allow) Authorize(context.Context, auth.Actor, *http.Request) bool { return true }

func TestResponses(t *testing.T) {
	cases := []struct {
		name         string
		authn        auth.Authenticator
		authz        auth.Authorizer
		method, path string
		header       map[string]string
		wantStatus   int
		wantCode     string
	}{
		// P1-01 ships deny-all hooks: every staff operation is refused until P1-02.
		{"staff operation without credentials", auth.DenyAll{}, auth.DenyAll{}, "GET", "/v0/me", nil, 401, "unauthorized"},
		{"staff mutation without credentials", auth.DenyAll{}, auth.DenyAll{}, "POST", "/v0/shows", map[string]string{"Idempotency-Key": "key-00000001"}, 401, "unauthorized"},
		{"authenticated but not authorized", allow{}, auth.DenyAll{}, "GET", "/v0/me", nil, 403, "forbidden"},
		{"public operation is anonymous", auth.DenyAll{}, auth.DenyAll{}, "GET", "/v0/public/shows", nil, 501, "not_implemented"},
		{"authorized contract operation not built yet", allow{}, allow{}, "GET", "/v0/me", nil, 501, "not_implemented"},
		{"unknown path", auth.DenyAll{}, auth.DenyAll{}, "GET", "/v0/public/nope", nil, 404, "not_found"},
		{"invalid path parameter", allow{}, allow{}, "GET", "/v0/shows/not-a-uuid", nil, 400, "bad_request"},
		{"panic becomes a problem", auth.DenyAll{}, auth.DenyAll{}, "GET", "/v0/public/network", nil, 500, "internal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStack(t, c.authn, c.authz)
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
			for k, v := range c.header {
				req.Header.Set(k, v)
			}
			rec := s.do(req)
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.wantStatus, rec.Body)
			}
			p := decode(t, rec)
			if p.Code != c.wantCode || p.Type != problem.TypeBase+strings.ReplaceAll(c.wantCode, "_", "-") {
				t.Errorf("problem = %+v, want code %s", p, c.wantCode)
			}
			if p.RequestID == "" || p.RequestID != rec.Header().Get(requestid.Header) {
				t.Errorf("problem requestId %q, header %q", p.RequestID, rec.Header().Get(requestid.Header))
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestRequestIDIsKeptWhenValidAndReplacedOtherwise(t *testing.T) {
	s := newStack(t, auth.DenyAll{}, auth.DenyAll{})
	for in, keep := range map[string]bool{"gateway-id-12345": true, "bad id\n with spaces": false, "": false} {
		req := httptest.NewRequest("GET", "/v0/public/shows", nil)
		req.Header.Set(requestid.Header, in)
		got := s.do(req).Header().Get(requestid.Header)
		if keep && got != in || !keep && (got == in || len(got) != 32) {
			t.Errorf("incoming %q → %q", in, got)
		}
	}
}

func TestObservability(t *testing.T) {
	s := newStack(t, allow{}, allow{})
	req := httptest.NewRequest("GET", "/v0/shows/0b0e2c34-6a7f-4c55-9b8c-5f4f5bb9c2f1", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Cookie", "session=secret-cookie")
	req.URL.RawQuery = "token=secret-query"
	s.do(req)

	// Metrics are labelled by route pattern, never by raw path.
	if got := testutil.ToFloat64(mustCounter(t, s.reg, "/v0/shows/{showId}", "501")); got != 1 {
		t.Errorf("relay_http_requests_total{route=/v0/shows/{showId},code=501} = %v, want 1", got)
	}

	// One JSON access-log line with the route, request ID, and trace ID, and no secrets.
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(s.logs.Bytes()), &line); err != nil {
		t.Fatalf("access log is not one JSON line: %q", s.logs)
	}
	for _, k := range []string{"request_id", "route", "status", "trace_id", "duration_ms"} {
		if _, ok := line[k]; !ok {
			t.Errorf("access log missing %q: %v", k, line)
		}
	}
	for _, secret := range []string{"secret-token", "secret-cookie", "secret-query"} {
		if strings.Contains(s.logs.String(), secret) {
			t.Errorf("access log contains %q", secret)
		}
	}

	// The server span is named after the route.
	spans := s.spans.Ended()
	if len(spans) != 1 || spans[0].Name() != "GET /v0/shows/{showId}" {
		names := []string{}
		for _, sp := range spans {
			names = append(names, sp.Name())
		}
		t.Errorf("spans = %v, want [GET /v0/shows/{showId}]", names)
	}
}

func mustCounter(t *testing.T, reg *prometheus.Registry, route, code string) prometheus.Collector {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "relay_http_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["route"] == route && labels["code"] == code {
				return prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "x"}, func() float64 { return m.GetCounter().GetValue() })
			}
		}
	}
	t.Fatalf("no relay_http_requests_total{route=%q,code=%q}", route, code)
	return nil
}

func TestCORS(t *testing.T) {
	s := newStack(t, auth.DenyAll{}, auth.DenyAll{})
	preflight := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("OPTIONS", "/v0/shows", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, idempotency-key")
		return s.do(req)
	}
	ok := preflight("https://admin.relay.localtest.me")
	if ok.Code != http.StatusNoContent || ok.Header().Get("Access-Control-Allow-Origin") != "https://admin.relay.localtest.me" ||
		!strings.Contains(ok.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") {
		t.Errorf("allowed origin preflight: %d %v", ok.Code, ok.Header())
	}
	if ok.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("credentials (cookies) must not be allowed")
	}
	bad := preflight("https://evil.example")
	if bad.Code != http.StatusForbidden || bad.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("other origin preflight: %d %v", bad.Code, bad.Header())
	}
}
