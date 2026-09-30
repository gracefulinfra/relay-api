package idempotency_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	"github.com/gracefulinfra/relay-api/internal/platform/db"
	"github.com/gracefulinfra/relay-api/internal/platform/idempotency"
	"github.com/gracefulinfra/relay-api/internal/platform/problem"
	"github.com/gracefulinfra/relay-api/internal/platform/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// fixture is a stack with the production middleware order (authenticate → route → authorize →
// idempotency) around a handler with a real side effect: it inserts a widget row through db.Querier,
// inside the request transaction.
type fixture struct {
	pool    *pgxpool.Pool
	srv     *httptest.Server
	allowed atomic.Bool // the Authorizer's answer
	calls   atomic.Int64
	// hook runs inside the handler after the insert (to block, fail, or panic).
	hook func(w http.ResponseWriter, r *http.Request) bool
}

type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (auth.Actor, bool) {
	id := r.Header.Get("X-Test-Actor")
	return auth.Actor{ID: id}, id != ""
}

type flagAuthz struct{ f *fixture }

func (z flagAuthz) Authorize(context.Context, auth.Actor, *http.Request) bool {
	return z.f.allowed.Load()
}

func newFixture(t *testing.T, o idempotency.Options) *fixture {
	t.Helper()
	f := &fixture{pool: testdb.New(t)}
	f.allowed.Store(true)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `CREATE TABLE catalog.widgets (id bigserial PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if o.TTL == 0 {
		o.TTL = 24 * time.Hour
	}
	if o.LockTimeout == 0 {
		o.LockTimeout = 5 * time.Second
	}
	o.MaxBody = 1 << 20
	o.Logger = slog.New(slog.DiscardHandler)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		var in struct{ Name string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			problem.Write(w, r, problem.BadRequest("bad json"))
			return
		}
		var id int64
		err := db.Querier(r.Context(), f.pool).QueryRow(r.Context(),
			`INSERT INTO catalog.widgets (name) VALUES ($1) RETURNING id`, in.Name).Scan(&id)
		if err != nil {
			t.Errorf("insert: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if f.hook != nil && !f.hook(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", fmt.Sprintf("/v0/widgets/%d", id))
		w.Header().Set("X-Not-Stored", "x")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": in.Name})
	})

	mux := http.NewServeMux()
	var h http.Handler = handler
	h = idempotency.Middleware(f.pool, o)(h)
	h = auth.Authorize(flagAuthz{f})(h)
	mux.Handle("POST /v0/widgets", h)
	mux.Handle("PUT /v0/widgets/{id}", h)
	f.srv = httptest.NewServer(auth.Authenticate(headerAuth{})(mux))
	t.Cleanup(f.srv.Close)
	return f
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (f *fixture) do(t *testing.T, method, path, actor, key, body string) resp {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if actor != "" {
		req.Header.Set("X-Test-Actor", actor)
	}
	if key != "" {
		req.Header.Set(idempotency.Header, key)
	}
	res, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, string(b)}
}

func (f *fixture) widgets(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM catalog.widgets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func problemCode(t *testing.T, r resp) string {
	t.Helper()
	var p problem.Problem
	if err := json.Unmarshal([]byte(r.body), &p); err != nil {
		t.Fatalf("not a problem document: %q", r.body)
	}
	return p.Code
}

// The P1-01 acceptance test: an idempotent POST replayed returns the same response with no duplicate
// side effect.
func TestReplayReturnsSameResponseWithoutDuplicateEffect(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	first := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`)
	if first.status != http.StatusCreated {
		t.Fatalf("first: %d %s", first.status, first.body)
	}
	second := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`)

	if second.status != first.status || second.body != first.body {
		t.Errorf("replay differs: %d %q, want %d %q", second.status, second.body, first.status, first.body)
	}
	if got, want := second.header.Get("Location"), first.header.Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if second.header.Get(idempotency.ReplayedHeader) != "true" || first.header.Get(idempotency.ReplayedHeader) != "" {
		t.Errorf("Idempotent-Replayed: first %q, second %q", first.header.Get(idempotency.ReplayedHeader), second.header.Get(idempotency.ReplayedHeader))
	}
	if second.header.Get("X-Not-Stored") != "" {
		t.Error("replay reproduced a header outside the stored allow-list")
	}
	if n := f.widgets(t); n != 1 {
		t.Errorf("widgets = %d, want 1 (no duplicate side effect)", n)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("handler calls = %d, want 1", n)
	}
}

func TestReuseWithDifferentBodyIsRejected(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`)
	r := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"two"}`)
	if r.status != http.StatusConflict || problemCode(t, r) != "idempotency_key_reused" {
		t.Fatalf("got %d %s, want 409 idempotency_key_reused", r.status, r.body)
	}
	if n := f.widgets(t); n != 1 {
		t.Errorf("widgets = %d, want 1", n)
	}
}

func TestKeysAreScopedToActorMethodAndResource(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	const key = "key-00000001"
	cases := []struct{ method, path, actor string }{
		{"POST", "/v0/widgets", "alice"},
		{"POST", "/v0/widgets", "bob"},    // another actor
		{"PUT", "/v0/widgets/7", "alice"}, // another method and resource
		{"PUT", "/v0/widgets/8", "alice"}, // another resource
	}
	for _, c := range cases {
		r := f.do(t, c.method, c.path, c.actor, key, `{"name":"x"}`)
		if r.status != http.StatusCreated || r.header.Get(idempotency.ReplayedHeader) != "" {
			t.Errorf("%s %s as %s: %d replayed=%q, want a fresh 201", c.method, c.path, c.actor, r.status, r.header.Get(idempotency.ReplayedHeader))
		}
	}
	if n := f.widgets(t); n != len(cases) {
		t.Errorf("widgets = %d, want %d", n, len(cases))
	}
}

func TestAuthorizationIsRecheckedBeforeReplay(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	if r := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`); r.status != http.StatusCreated {
		t.Fatalf("first: %d", r.status)
	}
	f.allowed.Store(false) // alice loses her grant
	r := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`)
	if r.status != http.StatusForbidden || strings.Contains(r.body, `"id"`) {
		t.Fatalf("replay after revocation: %d %s, want 403 without the stored body", r.status, r.body)
	}
}

func TestUnauthenticatedMutationIsDenied(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	r := f.do(t, "POST", "/v0/widgets", "", "key-00000001", `{"name":"one"}`)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", r.status)
	}
	if f.widgets(t) != 0 {
		t.Error("unauthenticated request had a side effect")
	}
}

func TestMissingOrMalformedKeyIsRejected(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	for _, key := range []string{"", "short", "has space in it", strings.Repeat("k", 129)} {
		r := f.do(t, "POST", "/v0/widgets", "alice", key, `{"name":"one"}`)
		if r.status != http.StatusBadRequest || problemCode(t, r) != "idempotency_key_required" {
			t.Errorf("key %q: %d %s, want 400 idempotency_key_required", key, r.status, r.body)
		}
	}
	if f.widgets(t) != 0 {
		t.Error("rejected request had a side effect")
	}
}

func TestServerErrorsAreNotStoredAndRollBack(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	var fail atomic.Bool
	fail.Store(true)
	f.hook = func(w http.ResponseWriter, _ *http.Request) bool {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return false
		}
		return true
	}
	if r := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`); r.status != http.StatusServiceUnavailable {
		t.Fatalf("first: %d", r.status)
	}
	if n := f.widgets(t); n != 0 {
		t.Fatalf("widgets after 503 = %d, want 0 (rolled back)", n)
	}
	fail.Store(false)
	r := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`)
	if r.status != http.StatusCreated || r.header.Get(idempotency.ReplayedHeader) != "" {
		t.Fatalf("retry: %d replayed=%q, want a fresh 201", r.status, r.header.Get(idempotency.ReplayedHeader))
	}
	if n := f.widgets(t); n != 1 {
		t.Errorf("widgets = %d, want 1", n)
	}
}

func TestPanicRollsBack(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	var boom atomic.Bool
	boom.Store(true)
	f.hook = func(http.ResponseWriter, *http.Request) bool {
		if boom.Load() {
			panic("boom")
		}
		return true
	}
	// No recovery middleware in this fixture: net/http's own recovery ends the connection.
	req, _ := http.NewRequestWithContext(context.Background(), "POST", f.srv.URL+"/v0/widgets", strings.NewReader(`{"name":"one"}`))
	req.Header.Set("X-Test-Actor", "alice")
	req.Header.Set(idempotency.Header, "key-00000001")
	if res, err := f.srv.Client().Do(req); err == nil {
		_ = res.Body.Close()
	}
	if n := f.widgets(t); n != 0 {
		t.Fatalf("widgets after panic = %d, want 0", n)
	}
	boom.Store(false)
	if r := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`); r.status != http.StatusCreated {
		t.Fatalf("retry after panic: %d", r.status)
	}
}

func TestConcurrentRequestsWithOneKeyRunOnce(t *testing.T) {
	f := newFixture(t, idempotency.Options{})
	f.hook = func(http.ResponseWriter, *http.Request) bool {
		time.Sleep(100 * time.Millisecond) // hold the key while the others arrive
		return true
	}
	const n = 8
	var wg sync.WaitGroup
	results := make([]resp, n)
	for i := range n {
		wg.Go(func() { results[i] = f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`) })
	}
	wg.Wait()
	replayed := 0
	for i, r := range results {
		if r.status != http.StatusCreated || r.body != results[0].body {
			t.Errorf("request %d: %d %q, want 201 %q", i, r.status, r.body, results[0].body)
		}
		if r.header.Get(idempotency.ReplayedHeader) == "true" {
			replayed++
		}
	}
	if replayed != n-1 {
		t.Errorf("replayed = %d, want %d", replayed, n-1)
	}
	if got := f.widgets(t); got != 1 {
		t.Errorf("widgets = %d, want 1", got)
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("handler calls = %d, want 1", got)
	}
}

func TestConcurrentRequestTimesOutAsInProgress(t *testing.T) {
	f := newFixture(t, idempotency.Options{LockTimeout: 100 * time.Millisecond})
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	f.hook = func(http.ResponseWriter, *http.Request) bool {
		entered <- struct{}{}
		<-release
		return true
	}
	done := make(chan resp)
	go func() { done <- f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`) }()
	<-entered
	r := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`)
	close(release)
	first := <-done
	if r.status != http.StatusConflict || problemCode(t, r) != "idempotency_key_in_progress" || r.header.Get("Retry-After") == "" {
		t.Errorf("waiting request: %d %s, want 409 idempotency_key_in_progress with Retry-After", r.status, r.body)
	}
	if first.status != http.StatusCreated {
		t.Errorf("first request: %d", first.status)
	}
	if n := f.widgets(t); n != 1 {
		t.Errorf("widgets = %d, want 1", n)
	}
}

func TestExpiredKeyStartsFresh(t *testing.T) {
	f := newFixture(t, idempotency.Options{TTL: 50 * time.Millisecond})
	f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"one"}`)
	time.Sleep(100 * time.Millisecond)
	// After expiry the key is forgotten, even with a different body: TTL expiry never becomes a
	// business uniqueness rule.
	r := f.do(t, "POST", "/v0/widgets", "alice", "key-00000001", `{"name":"two"}`)
	if r.status != http.StatusCreated || r.header.Get(idempotency.ReplayedHeader) != "" {
		t.Fatalf("after expiry: %d replayed=%q, want a fresh 201", r.status, r.header.Get(idempotency.ReplayedHeader))
	}
	if n := f.widgets(t); n != 2 {
		t.Errorf("widgets = %d, want 2", n)
	}
}

func TestSafeMethodsPassThrough(t *testing.T) {
	pool := testdb.New(t)
	called := false
	h := idempotency.Middleware(pool, idempotency.Options{TTL: time.Hour, LockTimeout: time.Second, MaxBody: 1024, Logger: slog.New(slog.DiscardHandler)})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if _, ok := db.TxFrom(r.Context()); ok {
				t.Error("GET got a request transaction")
			}
			w.WriteHeader(http.StatusOK)
		}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v0/widgets", nil))
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("GET: called=%v code=%d", called, rec.Code)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	pool := testdb.New(t)
	h := idempotency.Middleware(pool, idempotency.Options{TTL: time.Hour, LockTimeout: time.Second, MaxBody: 16, Logger: slog.New(slog.DiscardHandler)})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("handler ran") }))
	req := httptest.NewRequest("POST", "/v0/widgets", strings.NewReader(strings.Repeat("x", 17)))
	req.Header.Set(idempotency.Header, "key-00000001")
	req = req.WithContext(auth.WithActor(req.Context(), auth.Actor{ID: "alice"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rec.Code)
	}
}
