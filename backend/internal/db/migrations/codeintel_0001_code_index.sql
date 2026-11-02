-- P5 codeintel context: T0 index runs, current entity projection and edges (#25).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md §3.2.
-- repo_id/version_id/analysis_id are logical BIGINT references only (cross
-- context); index_run_id is a same-context FK. IDs are application-generated
-- snowflakes (D34). code_index_run is mutable (status/counters), code_entity and
-- code_edge are a current projection refreshed per index run.

CREATE TABLE code_index_run (
    id              BIGINT NOT NULL,
    version_id      BIGINT NULL,
    repo_id         BIGINT NOT NULL,
    commit_sha      TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    branch          TEXT NOT NULL DEFAULT '' CHECK (char_length(branch) <= 255),
    source          TEXT NOT NULL DEFAULT 'git' CHECK (source IN ('git')),
    status          TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'running', 'done', 'failed')),
    files_total     INTEGER NOT NULL DEFAULT 0 CHECK (files_total >= 0),
    files_indexed   INTEGER NOT NULL DEFAULT 0 CHECK (files_indexed >= 0),
    truncated       BOOLEAN NOT NULL DEFAULT false,
    stats           JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(stats) = 'object'),
    errors          JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(errors) = 'array'),
    started_at      TIMESTAMPTZ NULL,
    finished_at     TIMESTAMPTZ NULL,
    created_by      BIGINT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_code_index_run PRIMARY KEY (id)
);

-- At most one queued/running index run per repository (partial unique index:
-- PostgreSQL UNIQUE constraints cannot carry a WHERE predicate).
CREATE UNIQUE INDEX uk_code_index_run_active
    ON code_index_run (repo_id) WHERE status IN ('queued', 'running');
CREATE INDEX idx_code_index_run_repo ON code_index_run (repo_id, created_at DESC);
CREATE INDEX idx_code_index_run_version ON code_index_run (version_id, created_at DESC);

CREATE TABLE code_entity (
    id              BIGINT NOT NULL,
    repo_id         BIGINT NOT NULL,
    commit_sha      TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    index_run_id    BIGINT NULL REFERENCES code_index_run (id) ON DELETE SET NULL,
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
    source          TEXT NOT NULL CHECK (source IN ('git', 'ai')),
    confidence      TEXT NOT NULL DEFAULT 'high'
                    CHECK (confidence IN ('low', 'medium', 'high')),
    evidence        JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    meta            JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(meta) = 'object'),
    fact_key        TEXT NOT NULL DEFAULT '' CHECK (char_length(fact_key) <= 512),
    fact_version    INTEGER NOT NULL DEFAULT 1 CHECK (fact_version >= 1),
    analysis_id     BIGINT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_code_entity PRIMARY KEY (id)
);

-- One git file entity per (repo, commit, path); one current AI fact per key.
CREATE UNIQUE INDEX uk_code_entity_file
    ON code_entity (repo_id, commit_sha, path) WHERE kind = 'file' AND source = 'git';
CREATE UNIQUE INDEX uk_code_entity_fact
    ON code_entity (repo_id, commit_sha, fact_key) WHERE fact_key <> '';
CREATE INDEX idx_code_entity_kind ON code_entity (repo_id, commit_sha, kind, path);
CREATE INDEX idx_code_entity_path ON code_entity (repo_id, path);
CREATE INDEX idx_code_entity_analysis ON code_entity (analysis_id);

CREATE TABLE code_edge (
    id              BIGINT NOT NULL,
    repo_id         BIGINT NOT NULL,
    commit_sha      TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    analysis_id     BIGINT NULL,
    src_entity_id   BIGINT NOT NULL REFERENCES code_entity (id) ON DELETE CASCADE,
    dst_entity_id   BIGINT NOT NULL REFERENCES code_entity (id) ON DELETE CASCADE,
    rel             TEXT NOT NULL
                    CHECK (rel IN ('contains', 'imports', 'defines', 'calls', 'renders',
                                   'route_to_api')),
    source          TEXT NOT NULL CHECK (source IN ('git', 'ai')),
    confidence      TEXT NOT NULL DEFAULT 'high'
                    CHECK (confidence IN ('low', 'medium', 'high')),
    evidence        JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    meta            JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(meta) = 'object'),
    fact_key        TEXT NOT NULL DEFAULT '' CHECK (char_length(fact_key) <= 512),
    fact_version    INTEGER NOT NULL DEFAULT 1 CHECK (fact_version >= 1),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_code_edge PRIMARY KEY (id),
    CONSTRAINT uk_code_edge_src_dst_rel UNIQUE (src_entity_id, dst_entity_id, rel)
);

CREATE INDEX idx_code_edge_src ON code_edge (src_entity_id);
CREATE INDEX idx_code_edge_dst ON code_edge (dst_entity_id);
CREATE INDEX idx_code_edge_analysis ON code_edge (analysis_id);
