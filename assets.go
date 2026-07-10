// Package autospecdb embeds the SQL migration sources so the compiled binary
// can apply them without a repo checkout. The migrations/ directory remains the
// human-readable source of truth (and the manual psql fallback via
// scripts/apply.sh reads the same files).
package autospecdb

import "embed"

// Migrations holds migrations/*.sql, applied in filename order by
// internal/migrate. Filenames stored in autospec.schema_migrations are the
// basenames, byte-identical to the rows written by the shell-era apply.sh.
//
//go:embed migrations/*.sql
var Migrations embed.FS
