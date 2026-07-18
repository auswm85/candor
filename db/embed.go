// Package db embeds SQL migration files so the daemon can apply the schema
// without needing the source tree present at runtime.
package db

import "embed"

// MigrationFiles holds all SQL migrations, applied in lexical filename order.
//
//go:embed migrations/*.sql
var MigrationFiles embed.FS
