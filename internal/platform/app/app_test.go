package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gracefulinfra/relay-api/internal/platform/app"
	"github.com/gracefulinfra/relay-api/internal/platform/health"
	"github.com/gracefulinfra/relay-api/internal/platform/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func TestPrintConfigRedactsAndExits(t *testing.T) {
	t.Setenv("RELAY_DATABASE_URL", "postgres://relay:hunter2@nowhere.invalid/relay")
	var out bytes.Buffer
	a, done, err := app.Start(context.Background(), "relay-api", []string{"--print-config"}, &out)
	if err != nil || !done || a != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if strings.Contains(out.String(), "hunter2") || !strings.Contains(out.String(), "REDACTED") {
		t.Fatalf("--print-config output: %s", out.String())
	}
}

func TestInvalidConfigFailsStartup(t *testing.T) {
	t.Setenv("RELAY_DATABASE_URL", "")
	_, _, err := app.Start(context.Background(), "relay-api", nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "RELAY_DATABASE_URL") {
		t.Fatalf("err = %v", err)
	}
}

func readyz(t *testing.T, dbName string) (int, map[string]any) {
	t.Helper()
	t.Setenv("RELAY_DATABASE_URL", testdb.URL(dbName))
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	a, _, err := app.Start(context.Background(), "relay-api", nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	checks, err := a.ReadinessChecks()
	if err != nil {
		t.Fatal(err)
	}
	h := health.Handler(a.Telemetry.Registry, checks...)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)

	live := httptest.NewRecorder()
	h.ServeHTTP(live, httptest.NewRequest("GET", "/healthz", nil))
	if live.Code != http.StatusOK {
		t.Errorf("/healthz = %d", live.Code)
	}
	met := httptest.NewRecorder()
	h.ServeHTTP(met, httptest.NewRequest("GET", "/metrics", nil))
	if met.Code != http.StatusOK || !strings.Contains(met.Body.String(), "go_goroutines") {
		t.Errorf("/metrics = %d", met.Code)
	}
	return rec.Code, body
}

func TestReadyWhenMigrated(t *testing.T) {
	_, name := testdb.NewName(t)
	code, body := readyz(t, name)
	if code != http.StatusOK {
		t.Fatalf("/readyz = %d %v", code, body)
	}
}

func TestNotReadyWithPendingMigrations(t *testing.T) {
	name := testdb.NewEmpty(t).Config().ConnConfig.Database
	code, body := readyz(t, name)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d %v, want 503", code, body)
	}
	checks, _ := body["checks"].(map[string]any)
	if checks["migrations"] != "pending migrations" || checks["database"] != "ok" {
		t.Errorf("checks = %v", checks)
	}
}
