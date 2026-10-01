// Package distribution owns destinations and their capability matrix, such as YouTube (P2-09).
//
// It owns the PostgreSQL schema `distribution` and nothing else: other modules reach its data only through the
// Go interface this package exports, never through its tables (conventions, architecture rule 3).
// Empty until its slice lands; P1-01 creates it so the module boundaries exist from the start.
package distribution
