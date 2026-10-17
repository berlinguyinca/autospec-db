-- 006_model_token_usage.down.sql — operator rollback for 006.
--
-- Drops the model_token_usage view, then un-records the migration so a
-- subsequent migrate.Apply re-applies 006. events_raw data is untouched: this
-- is a read-only projection, so rolling it back loses nothing.
-- (Not applied by migrate — only ever run by an operator.)

drop view if exists autospec.model_token_usage;
delete from autospec.schema_migrations where filename = '006_model_token_usage.sql';
