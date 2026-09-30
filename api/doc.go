// Package api binds relay-api to the HTTP contract.
//
// The router, request and response types, and the StrictServerInterface are generated in
// relay-contracts (oapi-codegen, committed as the Go module gen/go; P0-04) and imported here at the
// pinned tag in go.mod (github.com/gracefulinfra/relay-contracts/gen/go). Nothing is generated from
// the spec in this repository. Changing the API means: update the spec in relay-contracts, release a
// tag, bump the module here.
//
// Unimplemented (unimplemented.gen.go) answers 501 for every operation; Server embeds it and
// overrides operations as modules implement them.
package api

//go:generate go run ./internal/genstubs unimplemented.gen.go

// Server implements the contract. Operations move from Unimplemented to real module handlers slice by
// slice, starting with P1-02.
type Server struct {
	Unimplemented
}
