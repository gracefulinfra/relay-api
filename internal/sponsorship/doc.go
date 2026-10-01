// Package sponsorship owns sponsor records, placements, ad markers, and the AdDecisionProvider adapter (Phase 3).
//
// It owns the PostgreSQL schema `sponsorship` and nothing else: other modules reach its data only through the
// Go interface this package exports, never through its tables (conventions, architecture rule 3).
// Empty until its slice lands; P1-01 creates it so the module boundaries exist from the start.
package sponsorship
