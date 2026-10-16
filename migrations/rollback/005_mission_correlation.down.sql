-- 005_mission_correlation.down.sql — operator rollback for 005.
--
-- Drops the correlation view and its two partial indexes, then un-records the
-- migration so a subsequent migrate.Apply re-applies 005. events_raw data is
-- untouched: this is a read-only projection, so rolling it back loses nothing.
-- (Not applied by migrate — only ever run by an operator.)

drop index if exists autospec.events_raw_mission_idx;
drop index if exists autospec.events_raw_execution_idx;
drop view if exists autospec.mission_events;
delete from autospec.schema_migrations where filename = '005_mission_correlation.sql';
