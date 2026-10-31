package staffauth_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	"github.com/gracefulinfra/relay-api/internal/platform/staffauth"
)

// A route with no policy entry is denied, even to a network admin, so forgetting an x-relay-authz
// declaration can never open an endpoint.
func TestRouteWithoutPolicyIsDenied(t *testing.T) {
	z := &staffauth.Authorizer{Policy: staffauth.Policy{"GET /v0/known": staffauth.Staff}, Log: slog.New(slog.DiscardHandler)}
	admin := auth.Actor{ID: "00000000-0000-0000-0000-000000000001", Enabled: true, NetworkAdmin: true}

	known := httptest.NewRequest(http.MethodGet, "/v0/known", nil)
	known.Pattern = "GET /v0/known"
	if !z.Authorize(t.Context(), admin, known) {
		t.Fatal("covered route denied to an enabled network admin")
	}
	unknown := httptest.NewRequest(http.MethodGet, "/v0/new-route", nil)
	unknown.Pattern = "GET /v0/new-route"
	if z.Authorize(t.Context(), admin, unknown) {
		t.Fatal("route with no policy was allowed")
	}
}

func TestPolicyFromContractCoversEveryOperation(t *testing.T) {
	p, err := staffauth.PolicyFromContract()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(p), len(ops(t)); got != want {
		t.Fatalf("policy has %d entries for %d operations", got, want)
	}
	if p["GET /v0/me"] != staffauth.Staff || p["PUT /v0/shows/{showId}/grants/{staffUserId}"] != staffauth.Admin {
		t.Fatalf("unexpected tiers: %v %v", p["GET /v0/me"], p["PUT /v0/shows/{showId}/grants/{staffUserId}"])
	}
}
