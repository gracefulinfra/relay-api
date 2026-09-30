// Package transcripts owns transcript revisions from faster-whisper, a hosted adapter, or manual upload (P1-07).
//
// It owns the PostgreSQL schema `transcripts` and nothing else: other modules reach its data only through the
// Go interface this package exports, never through its tables (conventions, architecture rule 3).
// Empty until its slice lands; P1-01 creates it so the module boundaries exist from the start.
package transcripts
