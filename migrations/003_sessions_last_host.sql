-- 003_sessions_last_host.sql — sessions.host is the LAST-SEEN host, not
-- max(host): a roaming machine (laptop moving home → office, or an
-- AUTOSPEC_DB_HOST_LABEL change) must show where the session emitted from
-- most recently, matching the last_step/last_issue/last_pr semantics.

create or replace view autospec.sessions as
select
    payload->>'session_id'                                   as session_id,
    (array_agg(payload->>'host' order by received_at desc)
        filter (where payload->>'host' is not null))[1]      as host,
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
