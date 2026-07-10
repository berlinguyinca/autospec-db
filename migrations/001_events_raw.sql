-- 001_events_raw.sql — the single wire-contract table.
-- Agents (role autospec_emit) only ever INSERT one jsonb payload per event
-- (contract: autospec.events.v1, owned by the autospec core repo). All typing
-- and normalization happens in views/migrations here — never in agents.

create schema if not exists autospec;

create table if not exists autospec.events_raw (
    id          bigint generated always as identity primary key,
    received_at timestamptz not null default now(),
    payload     jsonb not null
);

-- Idempotency: emit + spool-drain are at-least-once; replays must be harmless.
-- Text expression (no ::uuid cast) so a malformed uuid can never poison the
-- insert path — telemetry is lossy-by-design, inserts must not throw.
create unique index if not exists events_raw_event_uuid_uidx
    on autospec.events_raw ((payload->>'event_uuid'))
    where payload ? 'event_uuid';

create index if not exists events_raw_kind_received_idx
    on autospec.events_raw ((payload->>'kind'), received_at desc);

create index if not exists events_raw_session_idx
    on autospec.events_raw ((payload->>'session_id'), received_at desc);

create index if not exists events_raw_host_idx
    on autospec.events_raw ((payload->>'host'), received_at desc);

-- The ONLY entry point agents use: `select autospec.ingest(:'payload'::jsonb)`.
-- SECURITY DEFINER because ON CONFLICT requires SELECT on the arbiter index,
-- which the insert-only agent role deliberately lacks — the function runs as
-- the schema owner, agents get EXECUTE and nothing else (a leaked agent DSN
-- can only append events, never read or modify anything).
create or replace function autospec.ingest(p jsonb)
returns void
language sql
security definer
set search_path = autospec, pg_temp
as $$
    insert into autospec.events_raw(payload) values (p)
    on conflict ((payload->>'event_uuid')) where payload ? 'event_uuid'
    do nothing;
$$;

revoke all on function autospec.ingest(jsonb) from public;
