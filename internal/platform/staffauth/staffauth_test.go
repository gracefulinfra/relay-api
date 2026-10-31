package staffauth_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	relayapi "github.com/gracefulinfra/relay-contracts/gen/go/relayapi"

	"github.com/gracefulinfra/relay-api/internal/platform/access"
)

var param = regexp.MustCompile(`\{[^}]+\}`)

// ops lists every operation of the contract as (method, concrete path, public).
func ops(t *testing.T) [][3]string {
	t.Helper()
	doc, err := relayapi.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	var out [][3]string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			concrete := param.ReplaceAllStringFunc(path, func(string) string { return uuid.NewString() })
			out = append(out, [3]string{method, concrete, fmt.Sprint(strings.HasPrefix(path, "/public/"))})
		}
	}
	return out
}

func TestTokenValidation(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	for _, tc := range []struct {
		name string
		c    claims
		raw  string
		want int
	}{
		{"valid token, user is provisioned but disabled", claims{Subject: "valid"}, "", http.StatusForbidden},
		{"wrong issuer", claims{Subject: "x", Issuer: "https://evil.test/realms/relay-staff"}, "", http.StatusUnauthorized},
		{"wrong audience (an ID token for the admin app)", claims{Subject: "x", Audience: []string{"relay-admin"}}, "", http.StatusUnauthorized},
		{"expired", claims{Subject: "x", Expiry: now.Add(-time.Hour)}, "", http.StatusUnauthorized},
		{"not yet valid", claims{Subject: "x", NotBefore: now.Add(time.Hour)}, "", http.StatusUnauthorized},
		{"HS256", claims{Subject: "x", Alg: jose.HS256}, "", http.StatusUnauthorized},
		{"unknown key id", claims{Subject: "x"}, "", http.StatusUnauthorized},
		{"no subject", claims{Subject: ""}, "", http.StatusUnauthorized},
		{"garbage", claims{}, "not.a.jwt", http.StatusUnauthorized},
		{"alg none", claims{}, "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJ4In0.", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tok := tc.raw
			if tok == "" {
				if tc.name == "unknown key id" {
					// Signed by a key the IdP never published.
					other := newIDP(t)
					tok = other.token(t, tc.c)
				} else {
					tok = e.idp.token(t, tc.c)
				}
			}
			if code, _ := e.do(tok, http.MethodGet, "/me", ""); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}

	t.Run("missing or duplicate Authorization header", func(t *testing.T) {
		if code, _ := e.do("", http.MethodGet, "/me", ""); code != http.StatusUnauthorized {
			t.Fatalf("no header: %d", code)
		}
	})

	t.Run("rejected tokens never provision a user", func(t *testing.T) {
		var n int
		if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM platform.staff_users WHERE subject = 'x'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("users for rejected tokens = %d, err %v", n, err)
		}
	})

	t.Run("key rotation: a token signed by a new key is accepted without restart", func(t *testing.T) {
		e.idp.rotate(t, "k2")
		if code, _ := e.do(e.signIn("after-rotation"), http.MethodGet, "/me", ""); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (authenticated, disabled)", code)
		}
	})

	t.Run("logs never contain the token", func(t *testing.T) {
		tok := e.idp.token(t, claims{Subject: "x", Expiry: now.Add(-time.Hour)})
		e.do(tok, http.MethodGet, "/me", "")
		if strings.Contains(e.logs.String(), tok) || strings.Contains(e.logs.String(), strings.Split(tok, ".")[1]) {
			t.Fatal("a log line contains token material")
		}
	})
}

func TestFirstSignInProvisionsDisabledUserAndAudits(t *testing.T) {
	e := newEnv(t)
	tok := e.signIn("newbie")
	for range 2 { // the second request reuses the row
		if code, _ := e.do(tok, http.MethodGet, "/me", ""); code != http.StatusForbidden {
			t.Fatalf("disabled user /me = %d, want 403", code)
		}
	}
	var enabled, admin bool
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT enabled, network_admin, (SELECT count(*) FROM platform.staff_users) FROM platform.staff_users WHERE subject='newbie'`).Scan(&enabled, &admin, &n); err != nil {
		t.Fatal(err)
	}
	if enabled || admin || n != 1 {
		t.Fatalf("enabled=%v admin=%v users=%d", enabled, admin, n)
	}
	if got := e.auditActions(e.userID("newbie").String()); strings.Join(got, ",") != "staff_user.provision" {
		t.Fatalf("audit = %v", got)
	}
}

func TestConcurrentFirstSignInCreatesOneUser(t *testing.T) {
	e := newEnv(t)
	tok := e.signIn("racer")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { e.do(tok, http.MethodGet, "/me", "") })
	}
	wg.Wait()
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM platform.staff_users WHERE subject='racer'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("users = %d, err %v", n, err)
	}
}

func TestIdentityIsIssuerPlusSubject(t *testing.T) {
	e := newEnv(t)
	for _, iss := range []string{"https://a.test/realms/x", "https://b.test/realms/x"} {
		if _, err := e.pool.Exec(t.Context(), `INSERT INTO platform.staff_users (issuer, subject) VALUES ($1, 'same-sub')`, iss); err != nil {
			t.Fatalf("same subject in a second issuer must be a separate user: %v", err)
		}
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO platform.staff_users (issuer, subject) VALUES ('https://a.test/realms/x', 'same-sub')`); err == nil {
		t.Fatal("duplicate issuer+subject accepted")
	}
}

// Every non-public operation answers 403 to a disabled user, and 401 to no credentials.
func TestDisabledUserIsForbiddenEverywhere(t *testing.T) {
	e := newEnv(t)
	tok := e.signIn("disabled-user")
	e.do(tok, http.MethodGet, "/me", "")
	n := 0
	for _, op := range ops(t) {
		if op[2] == "true" {
			continue
		}
		n++
		if code, _ := e.do(tok, op[0], op[1], "{}"); code != http.StatusForbidden {
			t.Errorf("%s %s as disabled user = %d, want 403", op[0], op[1], code)
		}
		if code, _ := e.do("", op[0], op[1], "{}"); code != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials = %d, want 401", op[0], op[1], code)
		}
	}
	if n < 50 {
		t.Fatalf("only %d operations exercised", n)
	}
}

// Every contract operation reaches Authorize with a route pattern that has a policy, so a new route
// without an x-relay-authz entry fails here (and at startup).
func TestEveryRouteHasAPolicy(t *testing.T) {
	e := newEnv(t)
	admin := e.admin("root")
	for _, op := range ops(t) {
		code, body := e.do(admin, op[0], op[1], "{}")
		if op[2] == "true" {
			code, body = e.do("", op[0], op[1], "")
		}
		if code == http.StatusForbidden || code == http.StatusUnauthorized {
			t.Errorf("%s %s = %d with a network admin / anonymous public caller: %s", op[0], op[1], code, body)
		}
	}
}

func TestShowScopedAccess(t *testing.T) {
	e := newEnv(t)
	adminTok := e.admin("root")
	showA, showB := uuid.NewString(), uuid.NewString()

	prodTok := e.signIn("prod-a")
	e.do(prodTok, http.MethodGet, "/me", "")
	prodID := e.userID("prod-a")
	if code, body := e.do(adminTok, http.MethodPut, "/shows/"+showA+"/grants/"+prodID.String(), `{"role":"producer"}`); code != http.StatusOK {
		t.Fatalf("grant: %d %s", code, body)
	}

	// 501 means authorization passed and the (unimplemented) handler ran.
	for _, tc := range []struct {
		name, method, path string
		want               int
	}{
		{"producer on own show (producer+)", http.MethodPost, "/shows/" + showA + "/episodes", http.StatusNotImplemented},
		{"producer on another show", http.MethodPost, "/shows/" + showB + "/episodes", http.StatusForbidden},
		{"producer reads own show (viewer+)", http.MethodGet, "/shows/" + showA + "/episodes", http.StatusNotImplemented},
		{"producer reads another show", http.MethodGet, "/shows/" + showB + "/episodes", http.StatusForbidden},
		{"producer cannot edit grants (editor+)", http.MethodGet, "/shows/" + showA + "/grants", http.StatusForbidden},
		{"producer cannot put a grant (admin)", http.MethodPut, "/shows/" + showA + "/grants/" + prodID.String(), http.StatusForbidden},
		{"producer cannot list staff (admin)", http.MethodGet, "/staff-users", http.StatusForbidden},
		{"staff tier: any enabled user", http.MethodGet, "/shows", http.StatusNotImplemented},
		{"collection op needs the tier on some show", http.MethodGet, "/audit-events", http.StatusNotImplemented},
		{"episode route has no show resolver yet: denied", http.MethodPost, "/episodes/" + uuid.NewString() + "/publish", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := e.do(prodTok, tc.method, tc.path, "{}"); code != tc.want {
				t.Fatalf("%s %s = %d, want %d: %s", tc.method, tc.path, code, tc.want, body)
			}
		})
	}

	t.Run("show editor can list grants of own show only", func(t *testing.T) {
		edTok := e.signIn("ed-b")
		e.do(edTok, http.MethodGet, "/me", "")
		e.do(adminTok, http.MethodPut, "/shows/"+showB+"/grants/"+e.userID("ed-b").String(), `{"role":"show_editor"}`)
		if code, _ := e.do(edTok, http.MethodGet, "/shows/"+showB+"/grants", ""); code != http.StatusOK {
			t.Fatalf("own show = %d", code)
		}
		if code, _ := e.do(edTok, http.MethodGet, "/shows/"+showA+"/grants", ""); code != http.StatusForbidden {
			t.Fatalf("other show = %d", code)
		}
	})

	t.Run("revoking takes effect on the next request", func(t *testing.T) {
		if code, _ := e.do(adminTok, http.MethodDelete, "/shows/"+showA+"/grants/"+prodID.String(), ""); code != http.StatusNoContent {
			t.Fatalf("revoke = %d", code)
		}
		if code, _ := e.do(prodTok, http.MethodPost, "/shows/"+showA+"/episodes", "{}"); code != http.StatusForbidden {
			t.Fatalf("after revoke = %d, want 403", code)
		}
	})

	t.Run("disabling takes effect on the next request", func(t *testing.T) {
		e.do(adminTok, http.MethodPut, "/shows/"+showA+"/grants/"+prodID.String(), `{"role":"producer"}`)
		if _, err := e.pool.Exec(t.Context(), `UPDATE platform.staff_users SET enabled=false WHERE id=$1`, prodID); err != nil {
			t.Fatal(err)
		}
		if code, _ := e.do(prodTok, http.MethodGet, "/me", ""); code != http.StatusForbidden {
			t.Fatalf("disabled /me = %d", code)
		}
	})
}

func TestGrantLifecycleAuditAndEnable(t *testing.T) {
	e := newEnv(t)
	adminTok := e.admin("root")
	show := uuid.NewString()
	tok := e.signIn("newcomer")
	e.do(tok, http.MethodGet, "/me", "")
	id := e.userID("newcomer")

	if code, _ := e.do(tok, http.MethodGet, "/me", ""); code != http.StatusForbidden {
		t.Fatalf("before grant = %d, want 403", code)
	}
	path := "/shows/" + show + "/grants/" + id.String()
	if code, body := e.do(adminTok, http.MethodPut, path, `{"role":"viewer"}`); code != http.StatusOK {
		t.Fatalf("put = %d %s", code, body)
	}
	code, body := e.do(tok, http.MethodGet, "/me", "")
	if code != http.StatusOK {
		t.Fatalf("after grant /me = %d %s", code, body)
	}
	var me relayapi.Me
	if err := json.Unmarshal(body, &me); err != nil || len(me.Grants) != 1 || me.Grants[0].Role != relayapi.ShowRoleViewer || me.StaffUser.NetworkAdmin {
		t.Fatalf("me = %+v err %v", me, err)
	}

	e.do(adminTok, http.MethodPut, path, `{"role":"producer"}`)
	if code, _ := e.do(adminTok, http.MethodPut, path, `{"role":"owner"}`); code != http.StatusBadRequest {
		t.Fatalf("bad role = %d, want 400", code)
	}
	if code, _ := e.do(adminTok, http.MethodDelete, path, ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := e.do(adminTok, http.MethodDelete, path, ""); code != http.StatusNotFound {
		t.Fatalf("delete twice = %d, want 404", code)
	}
	if code, _ := e.do(adminTok, http.MethodPut, "/shows/"+show+"/grants/"+uuid.NewString(), `{"role":"viewer"}`); code != http.StatusNotFound {
		t.Fatalf("unknown user = %d, want 404", code)
	}

	rows, err := e.pool.Query(t.Context(), `SELECT action, coalesce(before->>'role',''), coalesce(after->>'role',''), actor_id, request_id, host(ip)
		FROM platform.audit_events WHERE resource_type='show_grant' AND resource_id=$1 ORDER BY id`, show+"/"+id.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var action, before, after, req, ip string
		var actor *uuid.UUID
		if err := rows.Scan(&action, &before, &after, &actor, &req, &ip); err != nil {
			t.Fatal(err)
		}
		if actor == nil || req == "" || ip != "203.0.113.9" {
			t.Fatalf("event %s: actor=%v request_id=%q ip=%q", action, actor, req, ip)
		}
		got = append(got, fmt.Sprintf("%s %s>%s", action, before, after))
	}
	want := "show_grant.put >viewer|show_grant.put viewer>producer|show_grant.revoke producer>"
	if strings.Join(got, "|") != want {
		t.Fatalf("audit =\n %s\nwant\n %s", strings.Join(got, "|"), want)
	}
	if acts := e.auditActions(id.String()); strings.Join(acts, ",") != "staff_user.provision,staff_user.enable" {
		t.Fatalf("user audit = %v", acts)
	}
}

func TestAuditContainsNoSecrets(t *testing.T) {
	e := newEnv(t)
	tok := e.admin("root")
	e.do(tok, http.MethodGet, "/staff-users", "")
	var all string
	if err := e.pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(row_to_json(a)::text, ' '), '') FROM platform.audit_events a`).Scan(&all); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{tok, "Bearer", "eyJ", issuer} {
		if strings.Contains(all, bad) {
			t.Fatalf("audit log contains %q", bad)
		}
	}
}

func TestListStaffUsersPaginates(t *testing.T) {
	e := newEnv(t)
	adminTok := e.admin("root")
	for i := range 4 {
		e.do(e.signIn(fmt.Sprintf("u%d", i)), http.MethodGet, "/me", "")
	}
	seen := map[string]bool{}
	cursor := ""
	for range 5 {
		p := "/staff-users?limit=2"
		if cursor != "" {
			p += "&cursor=" + cursor
		}
		code, body := e.do(adminTok, http.MethodGet, p, "")
		if code != http.StatusOK {
			t.Fatalf("page = %d %s", code, body)
		}
		var pg relayapi.StaffUserPage
		if err := json.Unmarshal(body, &pg); err != nil {
			t.Fatal(err)
		}
		for _, u := range pg.Items {
			seen[u.Id.String()] = true
		}
		next, err := pg.NextCursor.Get()
		if err != nil {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d users, want 5", len(seen))
	}
	if code, _ := e.do(adminTok, http.MethodGet, "/staff-users?cursor=%21%21", ""); code != http.StatusBadRequest {
		t.Fatalf("bad cursor = %d", code)
	}
}

func TestBootstrapAdmin(t *testing.T) {
	e := newEnv(t)
	e.do(e.signIn("first"), http.MethodGet, "/me", "")
	e.do(e.signIn("second"), http.MethodGet, "/me", "")

	if _, err := access.Bootstrap(t.Context(), e.pool, issuer, "ghost"); !errors.Is(err, access.ErrUnknownUser) {
		t.Fatalf("unknown user: %v", err)
	}

	// Concurrent runs: exactly one succeeds.
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for _, sub := range []string{"first", "second", "first", "second"} {
		wg.Go(func() {
			_, err := access.Bootstrap(t.Context(), e.pool, issuer, sub)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, access.ErrAdminExists):
				refused++
			default:
				t.Errorf("bootstrap: %v", err)
			}
		})
	}
	wg.Wait()
	if ok != 1 || refused != 3 {
		t.Fatalf("ok=%d refused=%d, want 1 and 3", ok, refused)
	}
	var winner string
	if err := e.pool.QueryRow(t.Context(), `SELECT subject FROM platform.staff_users WHERE network_admin AND enabled`).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	tok := e.signIn(winner)
	if code, _ := e.do(tok, http.MethodGet, "/staff-users", ""); code != http.StatusOK {
		t.Fatalf("bootstrapped admin /staff-users = %d", code)
	}
	var actors, n int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE actor_id IS NULL), count(*) FROM platform.audit_events WHERE action='staff_user.bootstrap_admin'`).Scan(&actors, &n); err != nil || n != 1 || actors != 1 {
		t.Fatalf("bootstrap audit rows=%d system-actor=%d err=%v", n, actors, err)
	}
}

func (e *env) auditActions(resourceID string) []string {
	e.t.Helper()
	rows, err := e.pool.Query(e.t.Context(), `SELECT action FROM platform.audit_events WHERE resource_type='staff_user' AND resource_id=$1 ORDER BY id`, resourceID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}
