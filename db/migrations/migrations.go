// Package migrations embeds the goose SQL migrations, so every binary carries the schema version it
// was built for (relay-migrate applies them; relay-api and relay-worker compare against them in /readyz).
package migrations

import "embed"

// FS holds the *.sql migrations.
//
//go:embed *.sql
var FS embed.FS
