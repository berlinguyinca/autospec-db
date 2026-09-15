-- 004_resilience_events.sql — additive read projection over the resilient
-- agent runtime lifecycle (autospec_core::resilience::EVENTS).
--
-- Pure projection, lossy-by-design and never a correctness source. The control
-- plane (autospec) decides correctness; this view only makes the mirrored
-- lifecycle observable. Agents emit the same autospec.events.v1 payloads; the
-- kinds below are the additive resilience namespace. Because events_raw is a
-- generic jsonb table and ingest() is kind-agnostic, these already land
-- without any schema change — this view just surfaces them.

create or replace view autospec.resilience_events as
select
    received_at,
    payload->>'kind'            as kind,
    payload->>'session_id'      as session_id,
    payload->>'host'            as host,
    payload->>'repo'            as repo,
    payload->>'work_id'         as work_id,
    payload->>'attempt_id'      as attempt_id,
    payload->>'checkpoint_id'   as checkpoint_id,
    payload->>'stream_id'       as stream_id,
    payload->>'candidate_id'    as candidate_id,
    payload->>'outcome'         as outcome,
    payload->>'detail'          as detail
from autospec.events_raw
where payload->>'kind' in (
    -- context guardian
    'context.threshold_reached','checkpoint.requested','checkpoint.persisted',
    'checkpoint.acknowledged','execution.resumed',
    -- durable work protocol
    'work.assigned','work.delivered','claim.acquired','claim.renewed',
    'claim.expired','attempt.started','attempt.completed','validation.completed',
    'review.completed',
    -- attention streams
    'attention.started','attention.progressed','attention.completed',
    -- memory map
    'memory.map_generated','memory.retrieved',
    -- verified learning
    'lesson.candidate_created','lesson.validated','lesson.promoted',
    'lesson.rejected'
)
order by received_at desc;

-- Convenience: resilience events for one session/work, newest first.
create or replace view autospec.resilience_session as
select * from autospec.resilience_events;
