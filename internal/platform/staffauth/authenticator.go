package staffauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gracefulinfra/relay-api/internal/platform/access/accessdb"
	"github.com/gracefulinfra/relay-api/internal/platform/audit"
	"github.com/gracefulinfra/relay-api/internal/platform/auth"
)

// clockSkew is the leeway for not-before. Expiry leeway is go-oidc's own.
const clockSkew = 30 * time.Second

// maxTokenBytes bounds what is handed to the JWT parser.
const maxTokenBytes = 8192

// OIDCConfig configures token validation.
type OIDCConfig struct {
	// Issuer is the exact iss claim of the relay-staff realm (for example
	// https://auth.relay.localtest.me/realms/relay-staff).
	Issuer string
	// JWKSURL is where signing keys are fetched. It differs from the issuer's public URL when the API
	// reaches Keycloak through an in-cluster service.
	JWKSURL string
	// Audience must appear in the token's aud claim (the relay-api client).
	Audience string
}

// Authenticator validates Keycloak access tokens and maps them to staff users.
type Authenticator struct {
	verifier *oidc.IDTokenVerifier
	audience string
	pool     *pgxpool.Pool
	log      *slog.Logger
	now      func() time.Time
}

// NewAuthenticator builds an Authenticator. Keys are fetched lazily and cached by go-oidc's RemoteKeySet,
// which refetches when a token names an unknown key ID, so Keycloak key rotation needs no restart and
// the API can start before Keycloak is up.
//
// Only RS256 is accepted: it is Keycloak's default, and fixing the algorithm list rules out "none" and
// HMAC-with-public-key confusion.
func NewAuthenticator(ctx context.Context, c OIDCConfig, pool *pgxpool.Pool, log *slog.Logger) (*Authenticator, error) {
	if c.Issuer == "" || c.JWKSURL == "" || c.Audience == "" {
		return nil, errors.New("staffauth: issuer, JWKS URL and audience are all required")
	}
	keys := oidc.NewRemoteKeySet(ctx, c.JWKSURL)
	v := oidc.NewVerifier(c.Issuer, keys, &oidc.Config{
		SkipClientIDCheck:    true, // aud is checked below against the resource-server audience
		SupportedSigningAlgs: []string{oidc.RS256},
	})
	return &Authenticator{verifier: v, audience: c.Audience, pool: pool, log: log, now: time.Now}, nil
}

// Authenticate implements auth.Authenticator. Every failure is a plain 401 for the caller; the reason
// is logged at debug without the token.
func (a *Authenticator) Authenticate(r *http.Request) (auth.Actor, bool) {
	raw, ok := bearer(r)
	if !ok {
		return auth.Actor{}, false
	}
	ctx := r.Context()
	tok, err := a.verifier.Verify(ctx, raw) // signature, issuer, exp, algorithm
	if err != nil {
		a.log.DebugContext(ctx, "token rejected", slog.String("reason", "verify"), slog.String("error", redactTokenErr(err)))
		return auth.Actor{}, false
	}
	if !slices.Contains(tok.Audience, a.audience) {
		a.log.DebugContext(ctx, "token rejected", slog.String("reason", "audience"))
		return auth.Actor{}, false
	}
	var claims struct {
		NotBefore         *float64 `json:"nbf"`
		Name              string   `json:"name"`
		PreferredUsername string   `json:"preferred_username"`
	}
	if err := tok.Claims(&claims); err != nil || tok.Subject == "" {
		return auth.Actor{}, false
	}
	if claims.NotBefore != nil {
		nbf := time.Unix(int64(*claims.NotBefore), 0)
		if a.now().Add(clockSkew).Before(nbf) {
			a.log.DebugContext(ctx, "token rejected", slog.String("reason", "not-before"))
			return auth.Actor{}, false
		}
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	u, err := a.resolve(ctx, tok.Issuer, tok.Subject, name)
	if err != nil {
		a.log.ErrorContext(ctx, "resolve staff user", slog.Any("error", err))
		return auth.Actor{}, false
	}
	return auth.Actor{ID: u.ID.String(), Enabled: u.Enabled, NetworkAdmin: u.NetworkAdmin}, true
}

// resolve finds the staff user for issuer+subject, creating a disabled one on first sign-in.
func (a *Authenticator) resolve(ctx context.Context, issuer, subject, displayName string) (accessdb.PlatformStaffUser, error) {
	q := accessdb.New(a.pool)
	u, err := q.GetStaffUserByIdentity(ctx, accessdb.GetStaffUserByIdentityParams{Issuer: issuer, Subject: subject})
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return u, fmt.Errorf("lookup: %w", err)
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return u, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	u, err = accessdb.New(tx).ProvisionStaffUser(ctx, accessdb.ProvisionStaffUserParams{
		Issuer: issuer, Subject: subject, DisplayName: truncate(displayName, 200),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent first request won; read what it created.
		return q.GetStaffUserByIdentity(ctx, accessdb.GetStaffUserByIdentityParams{Issuer: issuer, Subject: subject})
	}
	if err != nil {
		return u, fmt.Errorf("provision: %w", err)
	}
	// The issuer and subject are identifiers, not diffed: the event says who was created and that they
	// start disabled.
	if err := audit.Write(ctx, tx, audit.Event{
		Action: "staff_user.provision", ResourceType: "staff_user", ResourceID: u.ID.String(),
		After: map[string]any{"enabled": false, "networkAdmin": false},
	}); err != nil {
		return u, err
	}
	return u, tx.Commit(ctx)
}

func bearer(r *http.Request) (string, bool) {
	vals := r.Header.Values("Authorization")
	if len(vals) != 1 {
		return "", false
	}
	scheme, tok, ok := strings.Cut(vals[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" || len(tok) > maxTokenBytes || strings.ContainsAny(tok, " \t") {
		return "", false
	}
	return tok, true
}

func truncate(s string, limit int) string {
	s = strings.ToValidUTF8(s, "")
	for len(s) > limit {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// redactTokenErr keeps the failure class and drops anything that could echo token contents.
func redactTokenErr(err error) string {
	switch {
	case strings.Contains(err.Error(), "expired"):
		return "expired"
	case strings.Contains(err.Error(), "issuer"):
		return "issuer mismatch"
	case strings.Contains(err.Error(), "signature"), strings.Contains(err.Error(), "algorithm"):
		return "signature or algorithm"
	}
	return "invalid"
}
