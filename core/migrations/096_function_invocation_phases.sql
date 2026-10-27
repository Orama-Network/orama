-- =============================================================================
-- 096_function_invocation_phases.sql
--
-- An invocation record says where its duration went (bugboard bug-963).
--
-- The same function answered in 20 ms on one gateway and in 250 ms on another,
-- and its record could not say why. A read at the default "weak" consistency
-- is answered by the namespace's raft leader, so an invocation on a node that
-- is not the leader pays a round trip to it for every read; a burst queues on
-- the gateway's execution slots; a module that is not in memory is compiled or
-- read back from disk first. Each record now carries:
--
--   node_id   the peer ID of the node whose gateway ran the invocation
--   queue_ms  the wait for an execution slot
--   load_ms   fetching and compiling the module (0 when it was in memory)
--   db_calls  the database host calls the invocation made
--   db_ms     their summed wall time
--
-- Nullable, with no backfill: a record written before this migration, or by a
-- gateway not yet upgraded during a rolling upgrade, has NULL, which the API
-- returns as 0 and an empty node. The runner treats a repeated ADD COLUMN as
-- applied, so a re-run changes nothing.
-- =============================================================================

ALTER TABLE function_invocations ADD COLUMN node_id TEXT;
ALTER TABLE function_invocations ADD COLUMN queue_ms INTEGER;
ALTER TABLE function_invocations ADD COLUMN load_ms INTEGER;
ALTER TABLE function_invocations ADD COLUMN db_calls INTEGER;
ALTER TABLE function_invocations ADD COLUMN db_ms INTEGER;

INSERT OR IGNORE INTO schema_migrations(version) VALUES (96);
