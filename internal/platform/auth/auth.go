// Package auth holds the authentication and authorization hooks of the HTTP stack. P1-01 defines the
// seams and fails closed; P1-02 supplies the Keycloak OIDC Authenticator and the show-scoped RBAC
// Authorizer.
//
// Order in the stack: Authenticate (every /v0 request) → route match → Authorize (per operation) →
// idempotency. Authorization therefore runs again before an idempotent replay, so a user who lost a
// grant cannot read back a stored response.
package auth

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/gracefulinfra/relay-api/internal/platform/problem"
)

// Actor is who is calling. ID is stable (the staff user's Keycloak subject in P1-02) and scopes
// Idempotency-Key storage.
type Actor struct {
	ID        string
	Anonymous bool
	// Enabled is false for a staff user no admin has granted a role to yet (or one an admin disabled).
	// A disabled actor is authenticated but is refused by every Authorizer.
	Enabled      bool
	NetworkAdmin bool
}

// AnonymousActor is the actor for /v0/public/* requests.
var AnonymousActor = Actor{ID: "anonymous", Anonymous: true}

// Authenticator resolves the actor from a request. It returns ok=false for missing or invalid
// credentials; the middleware answers 401 without saying which.
type Authenticator interface {
	Authenticate(r *http.Request) (Actor, bool)
}

// Authorizer decides whether actor may perform the matched operation. r.Pattern is the route pattern
// (for example "POST /v0/shows/{showId}/episodes"), and path values are available through r.PathValue.
type Authorizer interface {
	Authorize(ctx context.Context, actor Actor, r *http.Request) bool
}

// DenyAll is the P1-01 default for both hooks: nothing outside /v0/public is reachable until P1-02.
type DenyAll struct{}

// Authenticate implements Authenticator.
func (DenyAll) Authenticate(*http.Request) (Actor, bool) { return Actor{}, false }

// Authorize implements Authorizer.
func (DenyAll) Authorize(context.Context, Actor, *http.Request) bool { return false }

type ctxKey struct{}

// WithActor stores the actor in ctx.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// ActorFrom returns the actor in ctx.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(ctxKey{}).(Actor)
	return a, ok
}

type clientIPKey struct{}

// ClientIPFrom returns the peer address recorded for audit events. It is the TCP peer, which behind the
// Gateway is the proxy; honouring a trusted X-Forwarded-For is a follow-up (docs/follow-ups.md).
func ClientIPFrom(ctx context.Context) (netip.Addr, bool) {
	a, ok := ctx.Value(clientIPKey{}).(netip.Addr)
	return a, ok
}

func withClientIP(r *http.Request) context.Context {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.Context()
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return r.Context()
	}
	return context.WithValue(r.Context(), clientIPKey{}, a.Unmap())
}

// PublicPrefix is the anonymous published-content API. It never varies by credentials, so credentials
// sent there are ignored rather than checked.
const PublicPrefix = "/v0/public/"

// Authenticate is the global middleware: anonymous for the public API, otherwise the Authenticator
// decides, and a failure is 401.
func Authenticate(a Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(withClientIP(r))
			if strings.HasPrefix(r.URL.Path, PublicPrefix) {
				next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), AnonymousActor)))
				return
			}
			actor, ok := a.Authenticate(r)
			if !ok || actor.ID == "" || actor.Anonymous {
				w.Header().Set("WWW-Authenticate", `Bearer realm="relay-staff"`)
				problem.Write(w, r, problem.Unauthorized())
				return
			}
			next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
		})
	}
}

// Permit reports whether the request may proceed to its handler: the same decision Authorize makes.
// The server also calls it before answering a request-validation error, so an unauthorized caller gets
// 403 rather than learning from a 400 what the operation expects.
func Permit(z Authorizer, r *http.Request) bool {
	actor, ok := ActorFrom(r.Context())
	if !ok {
		return false
	}
	if actor.Anonymous && strings.HasPrefix(r.URL.Path, PublicPrefix) {
		return true
	}
	return !actor.Anonymous && z.Authorize(r.Context(), actor, r)
}

// Authorize is the per-operation middleware (it runs after routing). Public operations are allowed;
// everything else asks the Authorizer, and a refusal is 403.
func Authorize(z Authorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := ActorFrom(r.Context()); !ok {
				problem.Write(w, r, problem.Unauthorized())
				return
			}
			if !Permit(z, r) {
				problem.Write(w, r, problem.Forbidden())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
