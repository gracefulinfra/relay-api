// Package problem writes RFC 9457 problem documents, the error format of every Relay API response
// (OpenAPI v0 `Problem`). Types are URIs under https://relay.dev/problems/.
package problem

import (
	"encoding/json"
	"net/http"

	"github.com/gracefulinfra/relay-api/internal/platform/requestid"
)

// TypeBase prefixes every problem type URI.
const TypeBase = "https://relay.dev/problems/"

// Problem is the wire shape. Extension members used by later slices (errors, blockingReasons,
// currentState) are added by those slices.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Code      string `json:"code,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// New builds a problem whose type is TypeBase + slug and whose code is the slug in snake case.
func New(status int, slug, title, detail string) Problem {
	return Problem{
		Type:   TypeBase + slug,
		Title:  title,
		Status: status,
		Detail: detail,
		Code:   snake(slug),
	}
}

// Write sends p with the request ID of r. Problem responses are never cached.
func Write(w http.ResponseWriter, r *http.Request, p Problem) {
	if p.RequestID == "" {
		p.RequestID = requestid.From(r.Context())
	}
	h := w.Header()
	h.Set("Content-Type", "application/problem+json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// Common problems.
var (
	NotFound = func(detail string) Problem {
		return New(http.StatusNotFound, "not-found", "Not found", detail)
	}
	BadRequest = func(detail string) Problem {
		return New(http.StatusBadRequest, "bad-request", "Bad request", detail)
	}
	Unauthorized = func() Problem {
		return New(http.StatusUnauthorized, "unauthorized", "Authentication required", "")
	}
	Forbidden = func() Problem {
		return New(http.StatusForbidden, "forbidden", "Forbidden", "")
	}
	Internal = func() Problem {
		return New(http.StatusInternalServerError, "internal", "Internal error", "")
	}
	NotImplemented = func() Problem {
		return New(http.StatusNotImplemented, "not-implemented", "Not implemented", "This operation is in the contract but not implemented yet.")
	}
)

func snake(slug string) string {
	b := []byte(slug)
	for i, c := range b {
		if c == '-' {
			b[i] = '_'
		}
	}
	return string(b)
}
