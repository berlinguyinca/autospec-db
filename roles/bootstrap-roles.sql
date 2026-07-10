-- bootstrap-roles.sql — least-privilege roles. Idempotent: safe to re-run;
-- passwords converge to the supplied values. Run as the schema owner
-- (install.sh does this for you):
--
--   psql "$ADMIN_DSN" \
--     -v emit_password="<strong-random-1>" \
--     -v read_password="<strong-random-2>" \
--     -f roles/bootstrap-roles.sql
--
-- autospec_emit: what agents carry on laptops. EXECUTE on autospec.ingest()
-- and NOTHING else (not even table INSERT) — a leaked DSN can only append
-- events through the idempotent ingest path, never read the telemetry corpus
-- or touch history.
-- autospec_read: what Grafana/Metabase uses. SELECT only.

-- NB: psql does not interpolate :'var' inside dollar-quoted DO bodies, so
-- creation (no secrets) is in the DO block and passwords are set via ALTER.
do $$
begin
    if not exists (select from pg_roles where rolname = 'autospec_emit') then
        create role autospec_emit login;
    end if;
    if not exists (select from pg_roles where rolname = 'autospec_read') then
        create role autospec_read login;
    end if;
end
$$;

alter role autospec_emit login password :'emit_password';
alter role autospec_read login password :'read_password';

grant usage   on schema autospec                 to autospec_emit;
grant execute on function autospec.ingest(jsonb) to autospec_emit;

grant usage  on schema autospec                   to autospec_read;
grant select on all tables in schema autospec     to autospec_read;
alter default privileges in schema autospec
    grant select on tables to autospec_read;
grant execute on function autospec.stalled_sessions(interval) to autospec_read;
