-- 002_views.sql — typed read layer over events_raw.
-- Stall math uses received_at (server clock), never client ts: a machine with
-- a drifting clock can not look falsely alive or dead.

-- One row per session: identity, latest activity, terminal state.
create or replace view autospec.sessions as
select
    payload->>'session_id'                                   as session_id,
    max(payload->>'host')                                    as host,
    max(payload->>'repo')                                    as repo,
    min(received_at)                                         as started_at,
    max(received_at)                                         as last_seen_at,
    max(received_at) filter (where payload->>'kind' = 'heartbeat')
                                                             as last_heartbeat_at,
    (array_agg(payload->>'step' order by received_at desc)
        filter (where payload->>'step' is not null))[1]      as last_step,
    (array_agg(payload->>'issue' order by received_at desc)
        filter (where payload->>'issue' is not null))[1]     as last_issue,
    (array_agg(payload->>'pr' order by received_at desc)
        filter (where payload->>'pr' is not null))[1]        as last_pr,
    bool_or(payload->>'kind' = 'session.terminal')           as is_terminal,
    bool_or(payload->>'kind' = 'session.parked')             as is_parked,
    (array_agg(payload->>'outcome' order by received_at desc)
        filter (where payload->>'kind' = 'session.terminal'))[1]
                                                             as terminal_outcome,
    count(*)                                                 as event_count
from autospec.events_raw
where payload->>'session_id' is not null
group by payload->>'session_id';

-- Stalled = not terminal, not parked, and silent for longer than the threshold.
create or replace function autospec.stalled_sessions(threshold interval default '5 minutes')
returns setof autospec.sessions
language sql stable as $$
    select * from autospec.sessions
    where not is_terminal
      and not is_parked
      and last_seen_at < now() - threshold
$$;

-- Convenience default-threshold view (Grafana-friendly).
create or replace view autospec.stalled as
select * from autospec.stalled_sessions();

-- Feature descriptions corpus: everything the pipeline designed, searchable.
create or replace view autospec.features as
select
    received_at,
    payload->>'repo'       as repo,
    payload->>'session_id' as session_id,
    payload->>'issue'      as issue,
    payload->>'detail'     as description
from autospec.events_raw
where payload->>'kind' = 'feature.described'
order by received_at desc;
