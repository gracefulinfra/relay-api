// Package catalog owns network, shows, seasons, episodes and their revisions, people, topics, staff users and show role grants, rights records, and chapter sets (P1-02, P1-03, P1-08).
//
// It owns the PostgreSQL schema `catalog` and nothing else: other modules reach its data only through the
// Go interface this package exports, never through its tables (conventions, architecture rule 3).
// Empty until its slice lands; P1-01 creates it so the module boundaries exist from the start.
package catalog
