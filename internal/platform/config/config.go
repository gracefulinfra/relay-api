// Package config loads process configuration from environment variables into a typed struct and
// validates it at startup. Secret fields carry the `secret:"true"` tag and are redacted by Redacted.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is shared by relay-api, relay-worker, and relay-migrate. Each binary uses the fields it needs.
type Config struct {
	// HTTPAddr serves the API (/v0). It is the only port the Gateway routes to.
	HTTPAddr string `env:"RELAY_HTTP_ADDR" envDefault:":8080" json:"httpAddr"`
	// OpsAddr serves /healthz, /readyz, and /metrics. It is never routed through the Gateway.
	OpsAddr string `env:"RELAY_OPS_ADDR" envDefault:":9090" json:"opsAddr"`

	DatabaseURL      string        `env:"RELAY_DATABASE_URL,required,notEmpty" secret:"true" json:"databaseUrl"`
	DBMaxConns       int32         `env:"RELAY_DB_MAX_CONNS" envDefault:"10" json:"dbMaxConns"`
	DBConnectTimeout time.Duration `env:"RELAY_DB_CONNECT_TIMEOUT" envDefault:"5s" json:"dbConnectTimeout"`

	LogLevel slog.Level `env:"RELAY_LOG_LEVEL" envDefault:"info" json:"logLevel"`

	// CORSAllowedOrigins is the allow-list of browser origins (the admin app). Empty disables CORS.
	CORSAllowedOrigins []string `env:"RELAY_CORS_ALLOWED_ORIGINS" envSeparator:"," json:"corsAllowedOrigins"`

	// IdempotencyTTL is how long a stored Idempotency-Key response is replayed (OpenAPI v0: 24 hours).
	IdempotencyTTL time.Duration `env:"RELAY_IDEMPOTENCY_TTL" envDefault:"24h" json:"idempotencyTtl"`
	// IdempotencyLockTimeout bounds how long a request waits for a concurrent request with the same key.
	IdempotencyLockTimeout time.Duration `env:"RELAY_IDEMPOTENCY_LOCK_TIMEOUT" envDefault:"5s" json:"idempotencyLockTimeout"`
	// MaxRequestBody caps request bodies on the JSON API. Uploads use tus (P1-04), not this path.
	MaxRequestBody int64 `env:"RELAY_MAX_REQUEST_BODY" envDefault:"1048576" json:"maxRequestBody"`

	ShutdownTimeout time.Duration `env:"RELAY_SHUTDOWN_TIMEOUT" envDefault:"20s" json:"shutdownTimeout"`

	// TracesExporter is "otlp" (OTEL_EXPORTER_OTLP_* configure the endpoint) or "none".
	TracesExporter string `env:"OTEL_TRACES_EXPORTER" envDefault:"otlp" json:"tracesExporter"`
	// OTLPEndpoint is read by the OTel SDK itself; it is listed here so --print-config shows it.
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT" json:"otlpEndpoint"`
}

// Load parses the environment and validates the result.
func Load() (Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return Config{}, friendly(err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks cross-field and format rules env parsing cannot express.
func (c Config) Validate() error {
	var errs []error
	u, err := url.Parse(c.DatabaseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		errs = append(errs, errors.New("RELAY_DATABASE_URL must be a postgres:// URL with a host"))
	}
	if c.DBMaxConns < 2 {
		errs = append(errs, errors.New("RELAY_DB_MAX_CONNS must be at least 2"))
	}
	for _, o := range c.CORSAllowedOrigins {
		ou, err := url.Parse(o)
		if o == "*" || err != nil || (ou.Scheme != "https" && ou.Scheme != "http") || ou.Host == "" || ou.Path != "" {
			errs = append(errs, fmt.Errorf("RELAY_CORS_ALLOWED_ORIGINS: %q is not an origin (scheme://host[:port]); wildcards are not allowed", o))
		}
	}
	if c.IdempotencyTTL < time.Hour {
		errs = append(errs, errors.New("RELAY_IDEMPOTENCY_TTL must be at least 1h"))
	}
	if c.IdempotencyLockTimeout <= 0 {
		errs = append(errs, errors.New("RELAY_IDEMPOTENCY_LOCK_TIMEOUT must be positive"))
	}
	if c.MaxRequestBody <= 0 {
		errs = append(errs, errors.New("RELAY_MAX_REQUEST_BODY must be positive"))
	}
	if c.TracesExporter != "otlp" && c.TracesExporter != "none" {
		errs = append(errs, errors.New(`OTEL_TRACES_EXPORTER must be "otlp" or "none"`))
	}
	return errors.Join(errs...)
}

// Redacted returns the config as a map for --print-config, with every secret field replaced. A URL
// keeps its shape and loses only its password, so the output still shows where it points.
func (c Config) Redacted() map[string]any {
	out := map[string]any{}
	v := reflect.ValueOf(c)
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		val := v.Field(i).Interface()
		if f.Tag.Get("secret") == "true" {
			val = redact(fmt.Sprint(val))
		} else if d, ok := val.(time.Duration); ok {
			val = d.String()
		} else if l, ok := val.(slog.Level); ok {
			val = l.String()
		}
		out[name] = val
	}
	return out
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "REDACTED")
		}
		q := u.Query()
		for k := range q {
			if strings.Contains(strings.ToLower(k), "password") {
				q.Set(k, "REDACTED")
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	return "REDACTED"
}

// friendly names the environment variable in parse errors (the library names the struct field).
func friendly(err error) error {
	var agg env.AggregateError
	if !errors.As(err, &agg) {
		return fmt.Errorf("config: %w", err)
	}
	t := reflect.TypeFor[Config]()
	var out []error
	for _, e := range agg.Errors {
		var pe env.ParseError
		if errors.As(e, &pe) {
			if f, ok := t.FieldByName(pe.Name); ok {
				name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
				out = append(out, fmt.Errorf("%s: %w", name, pe.Err))
				continue
			}
		}
		out = append(out, e)
	}
	return fmt.Errorf("config: %w", errors.Join(out...))
}
