// Package feeds owns RSS feed rendering and feed snapshots in object storage (P1-10).
//
// It owns the PostgreSQL schema `feeds` and nothing else: other modules reach its data only through the
// Go interface this package exports, never through its tables (conventions, architecture rule 3).
// Empty until its slice lands; P1-01 creates it so the module boundaries exist from the start.
package feeds
