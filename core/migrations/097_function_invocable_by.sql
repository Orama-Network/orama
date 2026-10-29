-- =============================================================================
-- 097_function_invocable_by.sql
--
-- A function may name the functions of its namespace that are allowed to
-- invoke it (function.yaml `invocable_by`, bugboard feat-971). Such a function
-- runs only when one of them calls it with function_invoke or
-- function_invoke_async; a client is refused on every route that reaches it.
--
--   invocable_by  the names, comma-separated (a function name holds no comma),
--                 or '' for none: any caller the usual rules admit
--
-- The value lives on the row of the version that declared it, like every other
-- function.yaml setting: a redeploy changes it, a rollback brings back the
-- rolled-back version's own.
--
-- Expand only: NOT NULL with a default of '', so every function deployed before
-- the column, and every one a gateway that predates it inserts during a rolling
-- upgrade, names no parent and is invoked exactly as before. Replay-safe: an
-- ADD COLUMN that already happened is "duplicate column name", which the runner
-- treats as applied.
-- =============================================================================

ALTER TABLE functions ADD COLUMN invocable_by TEXT NOT NULL DEFAULT '';

INSERT OR IGNORE INTO schema_migrations(version) VALUES (97);
