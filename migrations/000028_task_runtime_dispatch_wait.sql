-- migrate:up
-- Diagnostic only: Task business revision, clocks and execution authority stay unchanged.
ALTER TABLE tasks ADD COLUMN runtime_dispatch_wait jsonb;
-- migrate:down
ALTER TABLE tasks DROP COLUMN runtime_dispatch_wait;
