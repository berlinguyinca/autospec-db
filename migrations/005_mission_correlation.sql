-- 005_mission_correlation.sql — additive read projection over the
-- mission/task/execution correlation fields of autospec.events.v1 payloads
-- (spec §5 R-EVENT names: mission_id, task_id, execution_id).
--
-- Telemetry only, never authoritative (spec §4.5): the control plane
-- (autospec) decides correctness; this view only makes the mirrored
-- correlation ids observable. A pure projection over events_raw, so it lands
-- with no change to ingest() or role grants. Missing keys read as NULL — a
-- malformed or absent key can never throw, so a read is always safe.

create or replace view autospec.mission_events as
select
    received_at,
    payload->>'kind'         as kind,
    payload->>'session_id'   as session_id,
    payload->>'repo'         as repo,
    payload->>'mission_id'   as mission_id,
    payload->>'task_id'      as task_id,
    payload->>'execution_id' as execution_id
from autospec.events_raw
order by received_at desc;

-- Partial expression indexes, mirroring events_raw_session_idx but filtered so
-- the index only covers rows that actually carry the key. If not exists, so a
-- re-apply on an up-to-date server is a no-op.
create index if not exists events_raw_mission_idx
    on autospec.events_raw ((payload->>'mission_id'), received_at desc)
    where payload ? 'mission_id';

create index if not exists events_raw_execution_idx
    on autospec.events_raw ((payload->>'execution_id'), received_at desc)
    where payload ? 'execution_id';
