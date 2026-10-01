package config_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gracefulinfra/relay-api/internal/platform/config"
)

const dbURL = "postgres://relay:s3cret-pw@db.local:5432/relay?sslmode=require" //nolint:gosec // test fixture

func TestLoadDefaultsAndValidation(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"minimal", map[string]string{"RELAY_DATABASE_URL": dbURL}, ""},
		{"missing database URL", map[string]string{}, "RELAY_DATABASE_URL"},
		{"not a postgres URL", map[string]string{"RELAY_DATABASE_URL": "mysql://x/y"}, "postgres:// URL"},
		{"CORS wildcard", map[string]string{"RELAY_DATABASE_URL": dbURL, "RELAY_CORS_ALLOWED_ORIGINS": "*"}, "wildcards"},
		{"CORS with path", map[string]string{"RELAY_DATABASE_URL": dbURL, "RELAY_CORS_ALLOWED_ORIGINS": "https://a.example/app"}, "not an origin"},
		{"CORS list", map[string]string{"RELAY_DATABASE_URL": dbURL, "RELAY_CORS_ALLOWED_ORIGINS": "https://admin.relay.localtest.me,http://localhost:5173"}, ""},
		{"short TTL", map[string]string{"RELAY_DATABASE_URL": dbURL, "RELAY_IDEMPOTENCY_TTL": "5m"}, "at least 1h"},
		{"bad exporter", map[string]string{"RELAY_DATABASE_URL": dbURL, "OTEL_TRACES_EXPORTER": "zipkin"}, "OTEL_TRACES_EXPORTER"},
		{"bad duration", map[string]string{"RELAY_DATABASE_URL": dbURL, "RELAY_SHUTDOWN_TIMEOUT": "soon"}, "RELAY_SHUTDOWN_TIMEOUT"},
		{"too few conns", map[string]string{"RELAY_DATABASE_URL": dbURL, "RELAY_DB_MAX_CONNS": "1"}, "at least 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cfg, err := config.Load()
			if c.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if cfg.HTTPAddr != ":8080" || cfg.OpsAddr != ":9090" || cfg.IdempotencyTTL.Hours() != 24 {
					t.Errorf("defaults: %+v", cfg)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
			if strings.Contains(err.Error(), "s3cret-pw") {
				t.Error("error leaks the database password")
			}
		})
	}
}

func TestRedactedHidesSecrets(t *testing.T) {
	t.Setenv("RELAY_DATABASE_URL", dbURL)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cfg.Redacted())
	out := string(b)
	if strings.Contains(out, "s3cret-pw") {
		t.Fatalf("redacted config contains the password: %s", out)
	}
	if !strings.Contains(out, "relay:REDACTED@db.local:5432") {
		t.Errorf("redacted URL lost its shape: %s", out)
	}
	if !strings.Contains(out, `"idempotencyTtl":"24h0m0s"`) {
		t.Errorf("durations should print as strings: %s", out)
	}
}
