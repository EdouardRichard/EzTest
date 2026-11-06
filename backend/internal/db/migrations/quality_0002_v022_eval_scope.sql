-- EzTest v0.22 W2-14 (R-W2-6/W2-C5, spec §5 DB-26): scope snapshot evaluation
-- runs to a version and widen the mode check. Additive-only DDL: the existing
-- real_llm rows keep version_id NULL and the frozen metric names are unchanged.
-- Trend reads use (version_id, created_at).

ALTER TABLE eval_run ADD COLUMN version_id BIGINT NULL;

ALTER TABLE eval_run DROP CONSTRAINT IF EXISTS eval_run_mode_check;
ALTER TABLE eval_run
    ADD CONSTRAINT eval_run_mode_check CHECK (mode IN ('real_llm', 'snapshot'));

CREATE INDEX idx_eval_run_version_created ON eval_run (version_id, created_at DESC, id DESC);
