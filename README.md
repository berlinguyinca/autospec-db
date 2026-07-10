# autospec-db

Central Postgres telemetry for [autospec](https://github.com/berlinguyinca/autospec)
agents: every run/autonomous/fleet session streams lifecycle events (heartbeats, steps,
claims, filed artifacts, feature descriptions) to one database, so you can see what is
running where — across machines and sites — and spot stalled agents, without polling
GitHub (rate limits) or ssh-ing into hosts.

**100% optional.** Agents with `AUTOSPEC_DB_DSN` unset behave exactly as before — the
emit path is a no-op. Agents need only *outbound* connectivity to Postgres
(firewall-friendly; nothing listens on agent machines); offline agents spool locally
and drain on reconnect.

This repo ships a single static **`autospec-db`** binary (Go, no runtime deps — not even
`psql`) that provisions the database, applies migrations, converges least-privilege
roles, and is also the agent-side event emitter. The SQL migrations, role model, and
Grafana dashboards live here too, as the source of truth for the schema.

## Install (one line)

```bash
curl -fsSL https://raw.githubusercontent.com/berlinguyinca/autospec-db/main/install.sh | bash
```

The bootstrap downloads the prebuilt binary for your OS/arch to
`~/.autospec/bin/autospec-db` and runs `autospec-db install`:

- **First run** writes a config template to `~/.autospec/db.conf` (chmod 600) — fill in
  your Postgres host + admin credentials and run the same line again.
- **Second run** creates the database if missing, applies the schema + migrations
  (idempotent — re-runs only apply new ones), converges the least-privilege roles with
  generated passwords persisted in `db.conf`, verifies an end-to-end event through the
  agent role, and writes `~/.autospec/db.env`.
- `~/.autospec/db.env` is the ONLY thing agents need. Copy it to your other machines
  (`scp ~/.autospec/db.env host:~/.autospec/`) and every autospec run there reports in.
  Admin credentials never leave the machine you installed from.

Re-run the same line any time to pick up new migrations.

Bootstrap env overrides: `AUTOSPEC_DB_BINARY=<path>` uses a local binary instead of
downloading; `AUTOSPEC_DB_VERSION=vX.Y.Z` pins a release.

## Manual install (Go toolchain)

```bash
go install github.com/berlinguyinca/autospec-db/cmd/autospec-db@latest
autospec-db install
```

## Binary subcommands

| command | what it does |
|---|---|
| `install` | config template → create-db-if-missing → migrate → role convergence → `db.env` → verify |
| `migrate [--dsn <dsn>]` | apply embedded migrations (admin creds from `db.conf` by default) |
| `emit <kind> [k=v]...` | emit one event — fire-and-forget, never blocks or fails a run (`-v` prints the payload) |
| `drain` | replay the local spool into the database; prints `replayed/dropped/kept` |
| `sessions [--stalled] [--threshold 5m]` | aligned table of sessions (or the stalled subset) |
| `doctor` | OK/FAIL report: config, connectivity, TLS, migration status, spool size |
| `version` | print the binary version |

## Design notes

- **Wire contract is ONE call:** `select autospec.ingest($1::jsonb)` with a **bound**
  parameter (injection-safe — quotes / `$$` / backslashes in titles cannot inject). The
  function is `SECURITY DEFINER` and handles `event_uuid` dedup internally, so the agent
  role (`autospec_emit`) needs `EXECUTE` only — not even table `INSERT`. A leaked agent
  DSN can append events through the idempotent ingest path, never read the corpus or
  touch history.
- **Ingest-only blast radius:** `autospec_emit` has `USAGE` on the schema and `EXECUTE`
  on `autospec.ingest(jsonb)` and nothing else. `autospec_read` (dashboards) has
  `SELECT` only.
- **Optional + non-blocking:** with no DSN, `emit` exits 0 doing nothing. On any failure
  (connect/timeout/SQL) the payload is appended to `~/.autospec/db-spool.jsonl` and the
  process still exits 0. A successful emit opportunistically drains the spool. The spool
  is capped (`AUTOSPEC_DB_SPOOL_MAX_BYTES`, default 10 MB) and drops the oldest lines on
  overflow — telemetry is lossy-by-design, runs are not. Server *data* errors (bad cast)
  drop the offending line on drain (poison guard); connection errors keep it.
- Agents never couple to typed columns; the typed layer (views) migrates freely without
  breaking in-field emitters. The contract is additive-only within `autospec.events.v1`;
  unknown payload fields are ignored.

## What you get

- `autospec.events_raw` — append-only jsonb event stream (idempotent on `event_uuid`;
  spool replays are harmless)
- `autospec.sessions` — one row per session: host, repo, last step/issue/PR, last
  heartbeat, terminal/parked state
- `autospec.stalled` / `autospec.stalled_sessions(interval)` — sessions silent beyond a
  threshold with no terminal event; stall math uses server `received_at`, so agent
  clock skew cannot fake liveness
- `autospec.features` — searchable corpus of every feature description the pipeline
  generated

## Manual setup — existing Postgres server (psql fallback)

The binary is the recommended path; `scripts/apply.sh` is a raw-`psql` fallback that
applies the same migrations with the same `autospec.schema_migrations` tracking.

```bash
# 1. Schema (as an admin/owner DSN):
scripts/apply.sh "$ADMIN_DSN"

# 2. Roles — insert-only for agents, read-only for dashboards:
psql "$ADMIN_DSN" \
  -v emit_password="$(openssl rand -hex 16)" \
  -v read_password="$(openssl rand -hex 16)" \
  -f roles/bootstrap-roles.sql

# 3. On each agent machine (~/.autospec/db.env, chmod 600):
export AUTOSPEC_DB_DSN="postgresql://autospec_emit:<emit-password>@db.example.com:5432/autospec?sslmode=require&connect_timeout=2"
```

Point Grafana/Metabase at a DSN using `autospec_read` for dashboards.

Hardening checklist for a publicly reachable server: TLS (`sslmode=require`),
`scram-sha-256` auth, and ideally `pg_hba.conf` scoped to your sites (or put the port
behind a tailnet). The `autospec_emit` role can only execute `autospec.ingest()` — a
leaked agent DSN can append noise, never read or corrupt.

## Setup — no server yet (docker compose)

```bash
cp .env.example .env   # set POSTGRES_PASSWORD + READ_DSN_PASSWORD
docker compose up -d
autospec-db migrate --dsn "postgresql://postgres:$POSTGRES_PASSWORD@localhost:5432/autospec"
# then bootstrap roles (scripts/apply.sh path above) or run `autospec-db install`
```

Grafana comes up on `http://127.0.0.1:3000` with the datasource provisioned.

## License

MIT
