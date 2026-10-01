// Package media owns immutable, versioned, checksummed media assets and processing jobs (P1-04, P1-05).
//
// It owns the PostgreSQL schema `media` and nothing else: other modules reach its data only through the
// Go interface this package exports, never through its tables (conventions, architecture rule 3).
// Empty until its slice lands; P1-01 creates it so the module boundaries exist from the start.
package media
