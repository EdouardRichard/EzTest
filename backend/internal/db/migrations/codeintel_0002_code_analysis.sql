-- P5 codeintel context: immutable fact versions, change analysis runs and
-- impact candidates (#26). See
-- docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md §3.3.
-- code_fact rows are append-only versions (superseded_at marks history);
-- code_impact_candidate.analysis_id is a same-context FK, while
-- version_id/subject_id/created_by stay logical references (cross context).

CREATE TABLE code_fact (
    id              BIGINT NOT NULL,
    repo_id         BIGINT NOT NULL,
    commit_sha      TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    fact_key        TEXT NOT NULL CHECK (char_length(fact_key) BETWEEN 1 AND 512),
    version         INTEGER NOT NULL CHECK (version >= 1),
    kind            TEXT NOT NULL
                    CHECK (kind IN ('file', 'module', 'route', 'api', 'client_call',
                                    'component', 'page', 'symbol')),
    path            TEXT NOT NULL CHECK (char_length(path) BETWEEN 1 AND 4096),
    name            TEXT NOT NULL DEFAULT '' CHECK (char_length(name) <= 512),
    signature       TEXT NOT NULL DEFAULT '' CHECK (char_length(signature) <= 2000),
    start_line      INTEGER NULL CHECK (start_line IS NULL OR start_line >= 1),
    end_line        INTEGER NULL
                    CHECK (end_line IS NULL OR (start_line IS NOT NULL AND end_line >= start_line)),
    blob_hash       TEXT NOT NULL DEFAULT ''
                    CHECK (blob_hash = '' OR blob_hash ~ '^git-sha1:[0-9a-f]{40}$'
                           OR blob_hash ~ '^sha256:[0-9a-f]{64}$'),
    body            JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(body) = 'object'),
    evidence        JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    source          TEXT NOT NULL CHECK (source IN ('git', 'ai')),
    confidence      TEXT NOT NULL DEFAULT 'high'
                    CHECK (confidence IN ('low', 'medium', 'high')),
    model           TEXT NOT NULL DEFAULT '' CHECK (char_length(model) <= 128),
    prompt_version  TEXT NOT NULL DEFAULT '' CHECK (char_length(prompt_version) <= 64),
    analysis_id     BIGINT NULL,
    superseded_at   TIMESTAMPTZ NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_code_fact PRIMARY KEY (id),
    CONSTRAINT uk_code_fact_key_version UNIQUE (repo_id, commit_sha, fact_key, version)
);

CREATE INDEX idx_code_fact_lookup ON code_fact (repo_id, commit_sha, fact_key, version DESC);
CREATE INDEX idx_code_fact_kind ON code_fact (repo_id, commit_sha, kind, path);

CREATE TABLE code_analysis (
    id                  BIGINT NOT NULL,
    version_id          BIGINT NOT NULL,
    repo_id             BIGINT NOT NULL,
    base_commit_sha     TEXT NOT NULL CHECK (base_commit_sha ~ '^[0-9a-f]{40}$'),
    head_commit_sha     TEXT NOT NULL CHECK (head_commit_sha ~ '^[0-9a-f]{40}$'),
    status              TEXT NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued', 'running', 'done', 'failed')),
    degraded            BOOLEAN NOT NULL DEFAULT false,
    model               TEXT NOT NULL DEFAULT '' CHECK (char_length(model) <= 128),
    prompt_version      TEXT NOT NULL DEFAULT '' CHECK (char_length(prompt_version) <= 64),
    analysis_version    INTEGER NOT NULL DEFAULT 1 CHECK (analysis_version >= 1),
    changed_files       JSONB NOT NULL DEFAULT '[]'::jsonb
                        CHECK (jsonb_typeof(changed_files) = 'array'),
    stats               JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(stats) = 'object'),
    unmapped            JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(unmapped) = 'array'),
    errors              JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(errors) = 'array'),
    fact_versions       JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(fact_versions) = 'object'),
    created_by          BIGINT NULL,
    started_at          TIMESTAMPTZ NULL,
    finished_at         TIMESTAMPTZ NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_code_analysis PRIMARY KEY (id)
);

-- At most one queued/running analysis per (version, repo).
CREATE UNIQUE INDEX uk_code_analysis_active
    ON code_analysis (version_id, repo_id) WHERE status IN ('queued', 'running');
CREATE INDEX idx_code_analysis_version ON code_analysis (version_id, repo_id, id DESC);
CREATE INDEX idx_code_analysis_status ON code_analysis (status, id);

CREATE TABLE code_impact_candidate (
    id                      BIGINT NOT NULL,
    analysis_id             BIGINT NOT NULL REFERENCES code_analysis (id) ON DELETE CASCADE,
    subject_kind            TEXT NOT NULL
                            CHECK (subject_kind IN ('requirement', 'requirement_version',
                                                    'case_version', 'sut_page', 'sut_api')),
    subject_id              BIGINT NOT NULL,
    reason                  TEXT NOT NULL DEFAULT '' CHECK (char_length(reason) <= 2000),
    confidence              TEXT NOT NULL CHECK (confidence IN ('low', 'medium', 'high')),
    source                  TEXT NOT NULL CHECK (source IN ('direct', 'contract', 'relation', 'ai')),
    evidence                JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    selected                BOOLEAN NOT NULL DEFAULT true,
    selection_updated_by    BIGINT NULL,
    selection_updated_at    TIMESTAMPTZ NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_code_impact_candidate PRIMARY KEY (id),
    CONSTRAINT uk_code_impact_candidate_subject UNIQUE (analysis_id, subject_kind, subject_id)
);

CREATE INDEX idx_code_impact_candidate_subject
    ON code_impact_candidate (subject_kind, subject_id);
