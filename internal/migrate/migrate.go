// Package migrate applies the embedded migrations/*.sql files in filename
// order, tracking them in autospec.schema_migrations so an already-provisioned
// server (including one bootstrapped by the shell-era scripts/apply.sh) no-ops.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"

	"github.com/jackc/pgx/v5"
)

// bootstrapDDL creates the schema and the tracking table. Byte-compatible with
// the shell-era apply.sh: same table name, same columns, IF NOT EXISTS so an
// existing table is untouched.
const bootstrapDDL = `
create schema if not exists autospec;
create table if not exists autospec.schema_migrations (
    filename   text primary key,
    applied_at timestamptz not null default now()
);`

// Apply runs any embedded migrations not yet recorded, in filename order, and
// returns the basenames it applied (empty on an up-to-date server).
func Apply(ctx context.Context, conn *pgx.Conn, fsys fs.FS) ([]string, error) {
	if _, err := conn.Exec(ctx, bootstrapDDL); err != nil {
		return nil, fmt.Errorf("bootstrap schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		var seen int
		err := conn.QueryRow(ctx,
			"select 1 from autospec.schema_migrations where filename = $1", name,
		).Scan(&seen)
		if err == nil && seen == 1 {
			continue // already applied
		}
		if err != nil && err != pgx.ErrNoRows {
			return applied, fmt.Errorf("check migration %s: %w", name, err)
		}

		sqlBytes, rerr := fs.ReadFile(fsys, path.Join("migrations", name))
		if rerr != nil {
			return applied, fmt.Errorf("read migration %s: %w", name, rerr)
		}
		if _, eerr := conn.Exec(ctx, string(sqlBytes)); eerr != nil {
			return applied, fmt.Errorf("apply migration %s: %w", name, eerr)
		}
		if _, ierr := conn.Exec(ctx,
			"insert into autospec.schema_migrations(filename) values ($1)", name,
		); ierr != nil {
			return applied, fmt.Errorf("record migration %s: %w", name, ierr)
		}
		applied = append(applied, name)
	}
	return applied, nil
}

// Embedded returns the sorted basenames of the embedded migrations (for doctor).
func Embedded(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
