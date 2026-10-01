// Package requestid assigns every request an ID, returned in X-Request-Id, written to access logs, and
// set on problem documents and the request's trace span.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
)

// Header is the request and response header.
const Header = "X-Request-Id"

type ctxKey struct{}

// valid bounds an incoming ID (set by the Gateway) so logs cannot be polluted through it.
var valid = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

// Middleware keeps a well-formed incoming X-Request-Id, otherwise generates one.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(Header)
		if !valid.MatchString(id) {
			id = New()
		}
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(With(r.Context(), id)))
	})
}

// New returns a random 128-bit hex ID.
func New() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// With stores id in ctx.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From returns the request ID in ctx, or "".
func From(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}
