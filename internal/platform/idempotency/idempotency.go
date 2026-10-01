// Package idempotency implements the Idempotency-Key contract of OpenAPI v0 for every mutating
// operation (POST, PUT, PATCH, DELETE):
//
//   - A key is scoped to the actor, the method, and the request path (the resource), and binds the
//     SHA-256 of the request body.
//   - The first request claims the key and runs in one database transaction with it. The handler's
//     effects (through db.Querier) and the stored response commit together, or not at all.
//   - A concurrent request with the same key waits for the first to finish (the claim blocks on the
//     primary key), then replays its response. It waits at most RELAY_IDEMPOTENCY_LOCK_TIMEOUT and
//     then gets 409 idempotency-key-in-progress.
//   - A replay with the same body returns the stored status, headers, and body, with
//     Idempotent-Replayed: true. A different body gets 409 idempotency-key-reused.
//   - 5xx responses are not stored: the transaction rolls back, so the client may retry with the key.
//   - Authorization runs before this middleware, so it is rechecked on every replay.
//   - Rows expire after the TTL. Expiry only forgets the response; business uniqueness is enforced by
//     the business tables.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	"github.com/gracefulinfra/relay-api/internal/platform/db"
	"github.com/gracefulinfra/relay-api/internal/platform/idempotency/idempotencydb"
	"github.com/gracefulinfra/relay-api/internal/platform/problem"
)

// Header is the request header.
const Header = "Idempotency-Key"

// ReplayedHeader marks a replayed response.
const ReplayedHeader = "Idempotent-Replayed"

// keyPattern matches components/parameters/IdempotencyKey in OpenAPI v0.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

// storedHeaders are the response headers a replay reproduces. Everything else (Date, request IDs,
// tracing) belongs to the replaying request.
var storedHeaders = []string{"Content-Type", "Location", "ETag", "Cache-Control", "Preference-Applied"}

// Options configure the middleware.
type Options struct {
	TTL         time.Duration
	LockTimeout time.Duration
	MaxBody     int64
	Logger      *slog.Logger
}

// Middleware returns the per-operation idempotency middleware.
func Middleware(pool *pgxpool.Pool, o Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !mutating(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			serve(pool, o, next, w, r)
		})
	}
}

func mutating(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func serve(pool *pgxpool.Pool, o Options, next http.Handler, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := r.Header.Get(Header)
	if key == "" || len(r.Header.Values(Header)) != 1 || !keyPattern.MatchString(key) {
		problem.Write(w, r, problem.New(http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key required",
			"Mutating operations need exactly one Idempotency-Key header: 8-128 characters of A-Z a-z 0-9 . _ : -"))
		return
	}
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		problem.Write(w, r, problem.Unauthorized())
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, o.MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			problem.Write(w, r, problem.New(http.StatusRequestEntityTooLarge, "request-too-large", "Request body too large", ""))
			return
		}
		problem.Write(w, r, problem.BadRequest("could not read the request body"))
		return
	}
	sum := sha256.Sum256(body)
	r.Body = io.NopCloser(bytes.NewReader(body))

	scope := scope{Actor: actor.ID, Method: r.Method, Path: r.URL.EscapedPath(), Key: key}

	tx, err := pool.Begin(ctx)
	if err != nil {
		fail(w, r, o.Logger, "begin", err)
		return
	}
	// Rolls back on every path that does not commit, including a panic in the handler (the recovery
	// middleware above turns it into a 500).
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := idempotencydb.New(tx)
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", o.LockTimeout.Milliseconds())); err != nil {
		fail(w, r, o.Logger, "lock timeout", err)
		return
	}
	if err := q.DeleteExpiredIdempotencyKey(ctx, idempotencydb.DeleteExpiredIdempotencyKeyParams(scope)); err != nil {
		claimFailed(w, r, o.Logger, err)
		return
	}
	claimed, err := q.ClaimIdempotencyKey(ctx, idempotencydb.ClaimIdempotencyKeyParams{
		Actor: scope.Actor, Method: scope.Method, Path: scope.Path, Key: scope.Key,
		RequestHash: sum[:],
		Ttl:         pgtype.Interval{Microseconds: o.TTL.Microseconds(), Valid: true},
	})
	if err != nil {
		claimFailed(w, r, o.Logger, err)
		return
	}

	if claimed == 0 {
		replay(ctx, q, scope, sum[:], w, r, o.Logger)
		return
	}

	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	next.ServeHTTP(rec, r.WithContext(db.WithTx(ctx, tx)))

	if rec.status >= 500 || rec.status < 200 {
		// Not stored: the rollback undoes the handler's effects and releases the key.
		rec.flush(w)
		return
	}
	headers, _ := json.Marshal(pick(rec.header))
	err = q.CompleteIdempotencyKey(ctx, idempotencydb.CompleteIdempotencyKeyParams{
		Actor: scope.Actor, Method: scope.Method, Path: scope.Path, Key: scope.Key,
		ResponseStatus:  pgtype.Int2{Int16: int16(rec.status), Valid: true}, //nolint:gosec // 200-499
		ResponseHeaders: headers,
		ResponseBody:    rec.body.Bytes(),
	})
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		fail(w, r, o.Logger, "commit", err)
		return
	}
	rec.flush(w)
}

type scope struct {
	Actor, Method, Path, Key string
}

func replay(ctx context.Context, q *idempotencydb.Queries, s scope, sum []byte, w http.ResponseWriter, r *http.Request, log *slog.Logger) {
	row, err := q.GetIdempotencyKey(ctx, idempotencydb.GetIdempotencyKeyParams(s))
	if err != nil {
		claimFailed(w, r, log, err)
		return
	}
	if subtle.ConstantTimeCompare(row.RequestHash, sum) != 1 {
		problem.Write(w, r, problem.New(http.StatusConflict, "idempotency-key-reused", "Idempotency-Key reused",
			"This key was used with a different request body."))
		return
	}
	if !row.ResponseStatus.Valid {
		// Only completed rows are ever committed, so this is a bug, not a race.
		fail(w, r, log, "replay", errors.New("claimed key has no stored response"))
		return
	}
	var h map[string]string
	_ = json.Unmarshal(row.ResponseHeaders, &h)
	for k, v := range h {
		w.Header().Set(k, v)
	}
	w.Header().Set(ReplayedHeader, "true")
	w.WriteHeader(int(row.ResponseStatus.Int16))
	_, _ = w.Write(row.ResponseBody)
}

func claimFailed(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available
		w.Header().Set("Retry-After", "1")
		problem.Write(w, r, problem.New(http.StatusConflict, "idempotency-key-in-progress", "Idempotency-Key in progress",
			"A request with this key is still running. Retry shortly."))
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// The claiming transaction rolled back between our claim and our read. Safe to retry.
		w.Header().Set("Retry-After", "1")
		problem.Write(w, r, problem.New(http.StatusConflict, "idempotency-key-in-progress", "Idempotency-Key in progress", ""))
		return
	}
	fail(w, r, log, "claim", err)
}

func fail(w http.ResponseWriter, r *http.Request, log *slog.Logger, step string, err error) {
	log.ErrorContext(r.Context(), "idempotency failure", slog.String("step", step), slog.Any("error", err))
	problem.Write(w, r, problem.Internal())
}

func pick(h http.Header) map[string]string {
	out := map[string]string{}
	for _, k := range storedHeaders {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// recorder buffers the handler's response so it can be stored before anything reaches the client.
type recorder struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status, r.wroteHeader = code, true
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	return r.body.Write(b)
}

func (r *recorder) flush(w http.ResponseWriter) {
	for k, v := range r.header {
		w.Header()[k] = v
	}
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body.Bytes())
}
