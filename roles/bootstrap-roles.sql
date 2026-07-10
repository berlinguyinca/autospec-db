-- bootstrap-roles.sql — least-privilege roles. Run once as the schema owner:
--
--   psql "$ADMIN_DSN" \
--     -v emit_password="'<strong-random-1>'" \
--     -v read_password="'<strong-random-2>'" \
--     -f roles/bootstrap-roles.sql
--
-- autospec_emit: what agents carry on laptops. EXECUTE on autospec.ingest()
-- and NOTHING else (not even table INSERT) — a leaked DSN can only append
-- events through the idempotent ingest path, never read the telemetry corpus
-- or touch history.
-- autospec_read: what Grafana/Metabase uses. SELECT only.

create role autospec_emit login password :emit_password;
grant usage   on schema autospec                    to autospec_emit;
grant execute on function autospec.ingest(jsonb)    to autospec_emit;

create role autospec_read login password :read_password;
grant usage  on schema autospec              to autospec_read;
grant select on all tables in schema autospec to autospec_read;
alter default privileges in schema autospec
    grant select on tables to autospec_read;
grant execute on function autospec.stalled_sessions(interval) to autospec_read;
