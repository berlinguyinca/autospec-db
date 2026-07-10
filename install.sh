#!/usr/bin/env bash
# autospec-db one-line installer.
#
#   curl -fsSL https://raw.githubusercontent.com/berlinguyinca/autospec-db/main/install.sh | bash
#
# First run: writes a config template to ~/.autospec/db.conf and exits —
# fill in your Postgres admin credentials and re-run the same line.
# Subsequent runs (idempotent):
#   1. creates the database if it does not exist
#   2. applies migrations (schema, ingest(), views) — already-applied are skipped
#   3. converges the least-privilege roles (autospec_emit EXECUTE-only,
#      autospec_read SELECT-only); role passwords are generated once and
#      persisted in db.conf
#   4. writes ~/.autospec/db.env — the ONLY file agents need; copy it to your
#      other machines and every autospec run there starts reporting in
#
# Admin credentials are used by THIS script only; agents never see them.
# bash 3.2 compatible. Env overrides: AUTOSPEC_DB_CONF, AUTOSPEC_DB_SOURCE.
set -eu

CONF="${AUTOSPEC_DB_CONF:-$HOME/.autospec/db.conf}"
ENVF="$HOME/.autospec/db.env"
SRC="${AUTOSPEC_DB_SOURCE:-$HOME/.autospec/autospec-db}"
REPO="berlinguyinca/autospec-db"

say()  { printf 'autospec-db: %s\n' "$*"; }
die()  { printf 'autospec-db: ERROR: %s\n' "$*" >&2; exit 1; }

# ── 1. config file ──────────────────────────────────────────────────────────
if [ ! -f "$CONF" ]; then
    mkdir -p "$(dirname "$CONF")"
    cat > "$CONF" <<'EOF'
# ~/.autospec/db.conf — autospec-db connection settings. Keep chmod 600.
#
# Admin credentials are used ONLY by the installer (schema, migrations,
# roles). Agents use the generated ~/.autospec/db.env instead and can never
# read or modify telemetry.
DB_HOST=localhost
DB_PORT=5432
DB_NAME=autospec
DB_ADMIN_USER=postgres
DB_ADMIN_PASSWORD=
# require | prefer | disable  (use require for anything non-local)
DB_SSLMODE=prefer

# Generated on first successful install — do not edit:
EOF
    chmod 600 "$CONF"
    say "wrote config template: $CONF"
    say "→ edit it (at minimum DB_HOST + DB_ADMIN_PASSWORD), then re-run the same install line."
    exit 1
fi

# shellcheck disable=SC1090
. "$CONF"
[ -n "${DB_HOST:-}" ]           || die "DB_HOST missing in $CONF"
[ -n "${DB_PORT:-}" ]           || die "DB_PORT missing in $CONF"
[ -n "${DB_NAME:-}" ]           || die "DB_NAME missing in $CONF"
[ -n "${DB_ADMIN_USER:-}" ]     || die "DB_ADMIN_USER missing in $CONF"
[ -n "${DB_ADMIN_PASSWORD:-}" ] || die "DB_ADMIN_PASSWORD is empty in $CONF — set it and re-run"
DB_SSLMODE="${DB_SSLMODE:-prefer}"

command -v psql >/dev/null 2>&1 \
    || die "psql not found — install the Postgres client (macOS: brew install libpq && brew link --force libpq; debian: apt install postgresql-client)"

# ── 2. fetch migrations (skipped when AUTOSPEC_DB_SOURCE is a checkout) ─────
if [ ! -f "$SRC/migrations/001_events_raw.sql" ]; then
    say "fetching $REPO into $SRC"
    mkdir -p "$SRC"
    if command -v git >/dev/null 2>&1; then
        git clone --depth 1 "https://github.com/$REPO" "$SRC" 2>/dev/null \
            || git -C "$SRC" pull --ff-only 2>/dev/null || true
    fi
    if [ ! -f "$SRC/migrations/001_events_raw.sql" ]; then
        command -v curl >/dev/null 2>&1 || die "need git or curl to fetch $REPO"
        curl -fsSL "https://codeload.github.com/$REPO/tar.gz/refs/heads/main" \
            | tar -xz -C "$SRC" --strip-components=1
    fi
elif [ -d "$SRC/.git" ] && command -v git >/dev/null 2>&1; then
    git -C "$SRC" pull --ff-only -q 2>/dev/null || true   # best-effort refresh
fi
[ -f "$SRC/migrations/001_events_raw.sql" ] || die "no migrations found under $SRC"

# ── 3. connectivity + create-database-if-missing ────────────────────────────
# conninfo keyword form + PGPASSWORD: no URL-encoding pitfalls for admin creds.
export PGPASSWORD="$DB_ADMIN_PASSWORD"
admin_conn() { # $1 = dbname
    printf 'host=%s port=%s user=%s dbname=%s sslmode=%s connect_timeout=5' \
        "$DB_HOST" "$DB_PORT" "$DB_ADMIN_USER" "$1" "$DB_SSLMODE"
}

if ! psql "$(admin_conn "$DB_NAME")" -qtAc 'select 1' >/dev/null 2>&1; then
    say "database '$DB_NAME' not reachable — trying to create it"
    psql "$(admin_conn postgres)" -qtAc 'select 1' >/dev/null 2>&1 \
        || die "cannot reach Postgres at $DB_HOST:$DB_PORT as $DB_ADMIN_USER (checked dbs: $DB_NAME, postgres)"
    exists="$(printf '%s' "select 1 from pg_database where datname = :'db';" \
        | psql "$(admin_conn postgres)" -qtA -v db="$DB_NAME")"
    if [ "$exists" != "1" ]; then
        # identifier, not literal — quote_ident via format() in a DO block is
        # overkill here; validate instead (conf is operator-owned).
        case "$DB_NAME" in
            *[!a-zA-Z0-9_]*) die "DB_NAME must match [a-zA-Z0-9_]+ (got: $DB_NAME)" ;;
        esac
        psql "$(admin_conn postgres)" -q -c "create database $DB_NAME" \
            || die "could not create database $DB_NAME"
        say "created database $DB_NAME"
    fi
    psql "$(admin_conn "$DB_NAME")" -qtAc 'select 1' >/dev/null \
        || die "database $DB_NAME still not reachable after create"
fi

# ── 4. schema + migrations ──────────────────────────────────────────────────
bash "$SRC/scripts/apply.sh" "$(admin_conn "$DB_NAME")"

# ── 5. roles (passwords generated once, persisted in db.conf) ───────────────
gen_secret() {
    if command -v openssl >/dev/null 2>&1; then openssl rand -hex 16
    else od -An -N16 -tx1 /dev/urandom | tr -d ' \n'; fi
}
if [ -z "${EMIT_PASSWORD:-}" ]; then
    EMIT_PASSWORD="$(gen_secret)"
    printf 'EMIT_PASSWORD=%s\n' "$EMIT_PASSWORD" >> "$CONF"
fi
if [ -z "${READ_PASSWORD:-}" ]; then
    READ_PASSWORD="$(gen_secret)"
    printf 'READ_PASSWORD=%s\n' "$READ_PASSWORD" >> "$CONF"
fi
psql "$(admin_conn "$DB_NAME")" -v ON_ERROR_STOP=1 -q \
    -v emit_password="$EMIT_PASSWORD" -v read_password="$READ_PASSWORD" \
    -f "$SRC/roles/bootstrap-roles.sql"
say "roles converged (autospec_emit: ingest-only, autospec_read: select-only)"

# ── 6. agent env file + verification round-trip ─────────────────────────────
umask 177
cat > "$ENVF" <<EOF
# Generated by autospec-db install.sh — source this (autospec does it
# automatically). Copy this file to ~/.autospec/db.env on your other machines.
export AUTOSPEC_DB_DSN="postgresql://autospec_emit:$EMIT_PASSWORD@$DB_HOST:$DB_PORT/$DB_NAME?sslmode=$DB_SSLMODE&connect_timeout=2"
export AUTOSPEC_DB_READ_DSN="postgresql://autospec_read:$READ_PASSWORD@$DB_HOST:$DB_PORT/$DB_NAME?sslmode=$DB_SSLMODE&connect_timeout=2"
EOF
umask 022

uuid="$(gen_secret)"
probe="{\"schema\":\"autospec.events.v1\",\"event_uuid\":\"install-probe-$uuid\",\"kind\":\"session.terminal\",\"session_id\":\"install-probe\",\"host\":\"$(hostname -s 2>/dev/null || echo unknown)\",\"outcome\":\"install-verified\",\"ts\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}"
printf '%s' "select autospec.ingest(:'payload'::jsonb);" \
    | PGPASSWORD="$EMIT_PASSWORD" psql \
        "host=$DB_HOST port=$DB_PORT user=autospec_emit dbname=$DB_NAME sslmode=$DB_SSLMODE connect_timeout=5" \
        -v ON_ERROR_STOP=1 -q -v payload="$probe" >/dev/null \
    || die "verification emit through autospec_emit failed"
say "verification event ingested through the agent role"

say "DONE. Agents on this machine are configured via $ENVF"
say "→ other machines: copy $ENVF there (scp $ENVF host:~/.autospec/)"
say "→ dashboards: use AUTOSPEC_DB_READ_DSN from $ENVF (Grafana/Metabase)"
