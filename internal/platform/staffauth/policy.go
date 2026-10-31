// Package staffauth is staff authentication (Keycloak OIDC bearer tokens, issuer plus subject mapped to
// platform.staff_users) and show-scoped authorization (decision A9: Keycloak says who, Relay decides
// what).
//
// The policy table is the contract: every operation declares its minimum actor in `x-relay-authz`
// (relay-contracts OpenAPI v0), and Policy reads that table from the embedded spec. An operation with
// no declaration, or one this package does not understand, is denied. policy_test.go fails when a
// route in the contract has no policy.
package staffauth

import (
	"encoding/json"
	"fmt"
	"strings"

	relayapi "github.com/gracefulinfra/relay-contracts/gen/go/relayapi"

	"github.com/gracefulinfra/relay-api/internal/platform/server"
)

// Tier is the minimum actor an operation requires, in increasing order of privilege.
type Tier int

// The zero value is deliberately not a tier: an unset tier is a bug and is denied.
const (
	tierUnset Tier = iota
	// Public operations are anonymous (/v0/public/*); they are exempt from staff policy.
	Public
	// Staff is any enabled signed-in staff user.
	Staff
	// Viewer, Producer and Editor are grants on the operation's show: viewer, producer, show_editor.
	Viewer
	Producer
	Editor
	// Admin is networkAdmin.
	Admin
)

func (t Tier) String() string {
	switch t {
	case Public:
		return "public"
	case Staff:
		return "staff"
	case Viewer:
		return "viewer+"
	case Producer:
		return "producer+"
	case Editor:
		return "editor+"
	case Admin:
		return "admin"
	}
	return "unset"
}

func parseTier(s string) (Tier, bool) {
	for _, t := range []Tier{Public, Staff, Viewer, Producer, Editor, Admin} {
		if t.String() == s {
			return t, true
		}
	}
	return tierUnset, false
}

// roleRank is the tier a show role satisfies.
func roleRank(r relayapi.ShowRole) Tier {
	switch r {
	case relayapi.ShowRoleViewer:
		return Viewer
	case relayapi.ShowRoleProducer:
		return Producer
	case relayapi.ShowRoleShowEditor:
		return Editor
	}
	return tierUnset
}

// Policy maps a route pattern ("GET /v0/shows/{showId}") to its tier.
type Policy map[string]Tier

// PolicyFromContract builds the policy from the embedded OpenAPI document. An operation whose
// x-relay-authz is missing or unknown is an error, so a bad contract fails at startup and in tests
// rather than silently becoming deny-all or allow-all.
func PolicyFromContract() (Policy, error) {
	doc, err := relayapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("staffauth: load contract: %w", err)
	}
	p := Policy{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			raw := op.Extensions["x-relay-authz"]
			var name string
			switch v := raw.(type) {
			case string:
				name = v
			case json.RawMessage:
				_ = json.Unmarshal(v, &name)
			}
			t, ok := parseTier(name)
			if !ok {
				return nil, fmt.Errorf("staffauth: %s %s has no valid x-relay-authz (got %q)", method, path, name)
			}
			p[method+" "+server.BaseURL+path] = t
		}
	}
	return p, nil
}

// pathParams returns the wildcard names of a route pattern, in order.
func pathParams(pattern string) []string {
	var names []string
	for _, seg := range strings.Split(pattern, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}"))
		}
	}
	return names
}
