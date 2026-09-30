// Package analytics owns download analytics and the separate owned-experience and destination metric families (P1-17, P2-10).
//
// It owns the PostgreSQL schema `analytics` and nothing else: other modules reach its data only through the
// Go interface this package exports, never through its tables (conventions, architecture rule 3).
// Empty until its slice lands; P1-01 creates it so the module boundaries exist from the start.
package analytics
