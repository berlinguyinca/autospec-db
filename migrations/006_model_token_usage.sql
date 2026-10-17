-- 006_model_token_usage.sql — additive read projection that sums model and
-- token usage per mission from autospec.events.v1 payloads.
--
-- Telemetry only, never authoritative (spec §4.5): the control plane
-- (autospec) decides correctness; this view only makes the mirrored
-- model/token counts observable. A pure projection over events_raw, so it
-- lands with no change to ingest() or role grants.
--
-- Payload key names copy the autospec T08 event schema exactly (mission_id,
-- model, input_tokens, output_tokens) — nothing is invented here.
--
-- Casts are guarded by a digits regex: a malformed token count ("12x") reads
-- as NULL instead of raising, so a single bad event can never break every
-- read. Replayed events are deduplicated by events_raw's event_uuid unique
-- index, so they are counted once, never re-summed.

create or replace view autospec.model_token_usage as
select
    payload->>'mission_id'   as mission_id,
    payload->>'model'        as model,
    sum(case when payload->>'input_tokens'  ~ '^[0-9]+$'
             then (payload->>'input_tokens')::bigint  end) as input_tokens,
    sum(case when payload->>'output_tokens' ~ '^[0-9]+$'
             then (payload->>'output_tokens')::bigint end) as output_tokens
from autospec.events_raw
where payload ? 'mission_id' and payload ? 'model'
group by payload->>'mission_id', payload->>'model';
