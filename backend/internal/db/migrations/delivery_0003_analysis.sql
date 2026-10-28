-- P7 delivery context (spec §4.2.3): one row per failure-analysis or
-- issue-triage run. A partial unique index keeps at most one active run of a
-- kind per batch.
CREATE TABLE IF NOT EXISTS analysis_run (
  id               BIGINT NOT NULL,
  version_id       BIGINT NOT NULL,
  batch_id         BIGINT NOT NULL,
  kind             TEXT NOT NULL CHECK (kind IN ('failure','issue_triage')),
  status           TEXT NOT NULL DEFAULT 'queued'
                     CHECK (status IN ('queued','running','done','failed','degraded')),
  agent_session_id BIGINT,
  agent_run_id     BIGINT,
  model            TEXT NOT NULL DEFAULT '',
  prompt_version   TEXT NOT NULL DEFAULT '',
  input            JSONB,
  result           JSONB,
  stats            JSONB,
  errors           JSONB,
  created_by       BIGINT NOT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at       TIMESTAMPTZ,
  finished_at      TIMESTAMPTZ,
  CONSTRAINT pk_analysis_run PRIMARY KEY (id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_analysis_run_active
  ON analysis_run(batch_id, kind) WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS idx_analysis_run_batch
  ON analysis_run(batch_id, created_at DESC, id DESC);
