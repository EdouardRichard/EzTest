CREATE TABLE IF NOT EXISTS code_analyses (
  id              BIGINT PRIMARY KEY,
  version_id      BIGINT NOT NULL,
  repo_id         BIGINT NOT NULL,
  base_commit     TEXT NOT NULL CHECK (base_commit ~ '^[0-9a-f]{40}$'),
  head_commit     TEXT NOT NULL CHECK (head_commit ~ '^[0-9a-f]{40}$'),
  change_set_id   BIGINT,
  status          TEXT NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','running','done','failed')),
  degraded        BOOLEAN NOT NULL DEFAULT FALSE,
  model           TEXT NOT NULL DEFAULT '',
  prompt_version  TEXT NOT NULL CHECK (prompt_version <> ''),
  analysis_version INT NOT NULL DEFAULT 1 CHECK (analysis_version >= 1),
  fact_versions   JSONB NOT NULL DEFAULT '[]'::jsonb,
  stats           JSONB NOT NULL DEFAULT '{}'::jsonb,
  errors          JSONB NOT NULL DEFAULT '[]'::jsonb,
  unmapped        JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_by      BIGINT NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at      TIMESTAMPTZ,
  finished_at     TIMESTAMPTZ,
  UNIQUE (version_id, base_commit, head_commit, analysis_version)
);
CREATE INDEX IF NOT EXISTS idx_code_analyses_latest
  ON code_analyses(version_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_code_analyses_status
  ON code_analyses(status, created_at);

CREATE TABLE IF NOT EXISTS code_impact_candidates (
  id                   BIGINT PRIMARY KEY,
  analysis_id          BIGINT NOT NULL,
  subject_kind         TEXT NOT NULL CHECK (subject_kind IN ('requirement','app_model')),
  subject_id           BIGINT NOT NULL,
  reason               TEXT NOT NULL CHECK (reason <> '' AND length(reason) <= 2000),
  confidence           TEXT NOT NULL CHECK (confidence IN ('high','medium','low')),
  source               TEXT NOT NULL CHECK (source IN (
                         'direct','contract','relation','ai')),
  evidence             JSONB NOT NULL DEFAULT '[]'::jsonb,
  selected             BOOLEAN NOT NULL DEFAULT TRUE,
  selection_updated_by BIGINT,
  selection_updated_at TIMESTAMPTZ,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (analysis_id, subject_kind, subject_id)
);
CREATE INDEX IF NOT EXISTS idx_code_impact_candidates_analysis
  ON code_impact_candidates(analysis_id, subject_kind);

CREATE TABLE IF NOT EXISTS code_anchors (
  id              BIGINT PRIMARY KEY,
  version_id      BIGINT NOT NULL,
  repo_id         BIGINT NOT NULL,
  subject_kind    TEXT NOT NULL CHECK (subject_kind IN (
                    'version','requirement','app_model')),
  subject_id      BIGINT NOT NULL,
  file_path       TEXT NOT NULL CHECK (file_path <> '' AND length(file_path) <= 4096),
  start_line      INT CHECK (start_line IS NULL OR start_line >= 1),
  end_line        INT CHECK (end_line IS NULL OR end_line >= 1),
  entity_id       BIGINT,
  commit_sha      TEXT CHECK (commit_sha IS NULL OR commit_sha ~ '^[0-9a-f]{40}$'),
  confidence      TEXT NOT NULL DEFAULT 'high'
                  CHECK (confidence IN ('high','medium','low')),
  created_by_kind TEXT NOT NULL DEFAULT 'user' CHECK (created_by_kind IN ('user','ai')),
  created_by      BIGINT NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (end_line IS NULL OR start_line IS NULL OR end_line >= start_line)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_code_anchors_point
  ON code_anchors(version_id, subject_kind, subject_id, file_path,
                  COALESCE(start_line, 0), COALESCE(end_line, 0));
CREATE INDEX IF NOT EXISTS idx_code_anchors_subject
  ON code_anchors(version_id, subject_kind, subject_id);
CREATE INDEX IF NOT EXISTS idx_code_anchors_path
  ON code_anchors(repo_id, file_path);
