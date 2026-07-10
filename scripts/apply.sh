#!/usr/bin/env bash
# apply.sh — MANUAL psql FALLBACK. The `autospec-db` binary (migrate/install
# subcommands) is the primary path and embeds these same migrations. This
# script exists for operators who prefer raw psql or have no binary handy; it
# stays byte-compatible with the binary's autospec.schema_migrations tracking.
#
# apply.sh — apply migrations/*.sql in filename order, tracked in
# autospec.schema_migrations so re-runs are no-ops. bash 3.2 compatible.
#
#   scripts/apply.sh "$ADMIN_DSN"
set -eu

DSN="${1:?usage: scripts/apply.sh <admin-dsn>}"
HERE="$(cd "$(dirname "$0")/.." && pwd)"

psql "$DSN" -v ON_ERROR_STOP=1 -q -c "
    create schema if not exists autospec;
    create table if not exists autospec.schema_migrations (
        filename   text primary key,
        applied_at timestamptz not null default now()
    );"

applied=0
for f in "$HERE"/migrations/*.sql; do
    name="$(basename "$f")"
    # NB: psql does NOT interpolate :'var' inside -c strings — stdin only.
    seen="$(printf '%s' "select 1 from autospec.schema_migrations where filename = :'fname';" \
        | psql "$DSN" -v ON_ERROR_STOP=1 -qtA -v fname="$name")"
    if [ "$seen" = "1" ]; then
        echo "skip    $name (already applied)"
        continue
    fi
    echo "apply   $name"
    psql "$DSN" -v ON_ERROR_STOP=1 -q -f "$f"
    printf '%s' "insert into autospec.schema_migrations(filename) values (:'fname');" \
        | psql "$DSN" -v ON_ERROR_STOP=1 -q -v fname="$name"
    applied=$((applied + 1))
done
echo "done    ($applied applied)"
