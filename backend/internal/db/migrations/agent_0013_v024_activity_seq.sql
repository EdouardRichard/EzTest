-- v0.2.4-D activity fact ledger (T10, SC-D5 / design §2.3 Q04=A-lite): the
-- agent_activity table shipped in agent_0008 with only a plain (run_id, id)
-- index and no production reader or writer. This migration adds the idempotency
-- key the fact writes rely on: a per-run monotonic seq and UNIQUE(run_id, seq).
-- A tool call writes its row at its start and again with its outcome under the
-- SAME seq, so the write path is an UPSERT, never a duplicate.
--
-- Append-only: the column is added nullable, every existing row is backfilled
-- with its per-run ordinal in id order (so a legacy run keeps a valid, unique
-- key), and only then is NOT NULL applied. No production rows exist today; the
-- backfill keeps the migration correct for any deployment that has them.
ALTER TABLE agent_activity
    ADD COLUMN seq BIGINT;

UPDATE agent_activity AS activity
SET seq = ranked.seq
FROM (
    SELECT id, row_number() OVER (PARTITION BY run_id ORDER BY id) AS seq
    FROM agent_activity
) AS ranked
WHERE activity.id = ranked.id;

ALTER TABLE agent_activity
    ALTER COLUMN seq SET NOT NULL,
    ADD CONSTRAINT chk_agent_activity_seq CHECK (seq >= 1),
    ADD CONSTRAINT uk_agent_activity_run_seq UNIQUE (run_id, seq);
