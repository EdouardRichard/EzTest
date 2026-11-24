-- v0.2.4-C executor protocol v2 (T10): the per-binding executor reservation now
-- holds a v2 SESSION id instead of a v1 browser context id, plus the durable
-- recording-ledger settlement cursor. case_run gains the evidence-drop FACT
-- column that the verdict aggregation reads to degrade a run whose evidence was
-- lost to inconclusive (constitution art.4.5); the count must exist independently
-- of the evidence.skipped event stream (art.5.7).
--
-- Append-only: the v1 column is renamed, never rewritten in place, and both new
-- columns are NOT NULL with a zero default so existing rows stay valid. Sessions
-- created by the deleted v1 protocol are no longer reclaimable by key (the v1
-- surface is gone on both sides, R-C2); this is disclosed in the release notes.
ALTER TABLE case_run_sut_context RENAME COLUMN browser_context_id TO session_id;
ALTER INDEX idx_case_run_sut_context_context RENAME TO idx_case_run_sut_context_session;
ALTER TABLE case_run_sut_context ADD COLUMN artifact_cursor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE case_run ADD COLUMN evidence_dropped BIGINT NOT NULL DEFAULT 0;