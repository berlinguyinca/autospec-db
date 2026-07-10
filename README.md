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

This repo owns the database side: migrations, least-privilege roles, stall views, and
an optional docker-compose (Postgres + Grafana) for people without a server. The event
*payload contract* (`autospec.events.v1`) and the agent-side emitter live in the
autospec core repo — see
`docs/specs/2026-07-10-autospec-db-telemetry-design.md` there.

## Install (one line)

```bash
curl -fsSL https://raw.githubusercontent.com/berlinguyinca/autospec-db/main/install.sh | bash
```

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

## Manual setup — existing Postgres server


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

That is the entire agent-side setup. Point Grafana/Metabase at a DSN using
`autospec_read` for dashboards.

Hardening checklist for a publicly reachable server: TLS (`sslmode=require`),
`scram-sha-256` auth, and ideally `pg_hba.conf` scoped to your sites (or put the port
behind a tailnet). The `autospec_emit` role can only `INSERT` into
`autospec.events_raw` — a leaked agent DSN can append noise, never read or corrupt.

## Setup — no server yet

```bash
cp .env.example .env   # set POSTGRES_PASSWORD + READ_DSN_PASSWORD
docker compose up -d
scripts/apply.sh "postgresql://postgres:$POSTGRES_PASSWORD@localhost:5432/autospec"
psql ... -f roles/bootstrap-roles.sql   # as above
```

Grafana comes up on `http://127.0.0.1:3000` with the datasource provisioned.

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

## Design notes

- Wire contract is ONE call: `select autospec.ingest(:'payload'::jsonb)` with a bound
  psql variable (injection-safe). The function is SECURITY DEFINER and handles
  `event_uuid` dedup internally, so the agent role needs EXECUTE only — not even table
  INSERT — and live emits and spool-drain replays share one idempotent path.
- Agents never couple to typed columns; the typed layer (views) migrates freely without
  breaking in-field emitters.
- Contract is additive-only within `autospec.events.v1`; unknown payload fields are
  ignored (agents may run ahead of this schema).
- Inserts must never throw on odd payloads (no uuid casts in indexes) — telemetry is
  lossy-by-design, agent runs are not.

## License

MIT
