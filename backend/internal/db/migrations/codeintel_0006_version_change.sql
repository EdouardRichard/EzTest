-- P8 codeintel context (design spec §4.2/§7.2, D58): one per-batch code-change
-- snapshot. version_id/batch_id are logical references (no cross-context FK);
-- items stores a summary document only (file/changeKind/subjects/caseIds/
-- unmapped) and never code content, diffs, prompts or model responses. The
-- (version_id, batch_id) unique key makes the read-time upsert idempotent.
CREATE TABLE IF NOT EXISTS version_change (
  id                BIGINT PRIMARY KEY,
  version_id        BIGINT NOT NULL,
  batch_id          BIGINT NOT NULL,
  base_commit       TEXT NOT NULL,
  head_commit       TEXT NOT NULL,
  history_rewritten BOOLEAN NOT NULL DEFAULT FALSE,
  changed_files     INTEGER NOT NULL DEFAULT 0,
  impacted_cases    INTEGER NOT NULL DEFAULT 0,
  unmapped_count    INTEGER NOT NULL DEFAULT 0,
  items             JSONB NOT NULL DEFAULT '[]'::jsonb,
  computed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (version_id, batch_id)
);
