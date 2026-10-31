package staffauth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/gracefulinfra/relay-api/api"
	"github.com/gracefulinfra/relay-api/internal/platform/access"
	"github.com/gracefulinfra/relay-api/internal/platform/idempotency"
	"github.com/gracefulinfra/relay-api/internal/platform/server"
	"github.com/gracefulinfra/relay-api/internal/platform/staffauth"
	"github.com/gracefulinfra/relay-api/internal/platform/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const (
	issuer   = "https://auth.test/realms/relay-staff"
	audience = "relay-api"
)

// idp is a fake Keycloak: it serves a JWKS that tests can rotate, and mints tokens.
type idp struct {
	mu   sync.Mutex
	keys map[string]*rsa.PrivateKey // kid → key currently published
	srv  *httptest.Server
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	p := &idp{keys: map[string]*rsa.PrivateKey{}}
	p.rotate(t, "k1")
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		var set jose.JSONWebKeySet
		for kid, k := range p.keys {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &k.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"})
		}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// rotate publishes only a new key kid, withdrawing the others.
func (p *idp) rotate(t *testing.T, kid string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.keys = map[string]*rsa.PrivateKey{kid: k}
	p.mu.Unlock()
}

type claims struct {
	Issuer    string
	Audience  []string
	Subject   string
	Expiry    time.Time
	NotBefore time.Time
	Name      string
	KID       string
	Alg       jose.SignatureAlgorithm
}

// token mints a signed token; zero fields take valid defaults.
func (p *idp) token(t *testing.T, c claims) string {
	t.Helper()
	if c.Issuer == "" {
		c.Issuer = issuer
	}
	if c.Audience == nil {
		c.Audience = []string{audience}
	}
	if c.Expiry.IsZero() {
		c.Expiry = time.Now().Add(5 * time.Minute)
	}
	if c.Alg == "" {
		c.Alg = jose.RS256
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	kid := c.KID
	var key *rsa.PrivateKey
	for k, v := range p.keys {
		if kid == "" || kid == k {
			kid, key = k, v
		}
	}
	var signKey any = key
	if c.Alg == jose.HS256 {
		signKey = []byte("0123456789abcdef0123456789abcdef")
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: c.Alg, Key: signKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	cl := jwt.Claims{Issuer: c.Issuer, Subject: c.Subject, Audience: c.Audience, Expiry: jwt.NewNumericDate(c.Expiry)}
	if !c.NotBefore.IsZero() {
		cl.NotBefore = jwt.NewNumericDate(c.NotBefore)
	}
	raw, err := jwt.Signed(sig).Claims(cl).Claims(map[string]any{"name": c.Name}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// env is the real stack: authenticator, authorizer, access service, router, and a database.
type env struct {
	t    *testing.T
	idp  *idp
	pool *pgxpool.Pool
	h    http.Handler
	logs *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, idp: newIDP(t), pool: testdb.New(t), logs: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	authn, err := staffauth.NewAuthenticator(t.Context(), staffauth.OIDCConfig{Issuer: issuer, JWKSURL: e.idp.srv.URL, Audience: audience}, e.pool, log)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := staffauth.PolicyFromContract()
	if err != nil {
		t.Fatal(err)
	}
	e.h = server.New(&api.Server{Access: &access.Service{Pool: e.pool}}, server.Options{
		Pool: e.pool, Logger: log, Registerer: prometheus.NewRegistry(),
		Authenticator: authn,
		Authorizer:    &staffauth.Authorizer{Policy: policy, Pool: e.pool, Log: log},
		Idempotency:   idempotency.Options{TTL: time.Hour, LockTimeout: time.Second, MaxBody: 1 << 20, Logger: log},
	})
	return e
}

// do sends a request as the bearer. Mutating requests get a fresh Idempotency-Key.
func (e *env) do(bearer, method, path, body string) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, "/v0"+path, rd)
	req.RemoteAddr = "203.0.113.9:4444"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", "test-"+uuid.NewString())
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code >= 500 && rec.Code != http.StatusNotImplemented {
		e.t.Logf("%s %s = %d; log: %s", method, path, rec.Code, e.logs.String())
	}
	return rec.Code, rec.Body.Bytes()
}

// signIn returns a valid token for subject (provisioning the user on first use).
func (e *env) signIn(subject string) string {
	return e.idp.token(e.t, claims{Subject: subject, Name: "Person " + subject})
}

// userID returns the staff user ID for subject after it has signed in.
func (e *env) userID(subject string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(e.t.Context(), `SELECT id FROM platform.staff_users WHERE issuer=$1 AND subject=$2`, issuer, subject).Scan(&id); err != nil {
		e.t.Fatalf("user %s: %v", subject, err)
	}
	return id
}

// admin signs in subject and makes them the network admin through the bootstrap path.
func (e *env) admin(subject string) string {
	e.t.Helper()
	tok := e.signIn(subject)
	e.do(tok, http.MethodGet, "/me", "") // provisions
	if _, err := access.Bootstrap(e.t.Context(), e.pool, issuer, subject); err != nil {
		e.t.Fatal(err)
	}
	return tok
}
