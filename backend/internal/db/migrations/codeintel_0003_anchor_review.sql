-- P5 codeintel context: unified code anchors and append-only fact reviews (#27).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md §3.4.
-- code_anchor entity_id/commit_sha and all subject ids are logical references
-- (no cross-context FK); code_fact_review.fact_id is a same-context FK.
-- code_fact_review is append-only: updated_at is initialized on insert and never
-- updated by the repository (D34 §6.9.4).

CREATE TABLE code_anchor (
    id                  BIGINT NOT NULL,
    version_id          BIGINT NOT NULL,
    repo_id             BIGINT NOT NULL,
    subject_kind        TEXT NOT NULL
                        CHECK (subject_kind IN ('version', 'requirement', 'requirement_version',
                                                'case_version', 'issue')),
    subject_id          BIGINT NOT NULL,
    file_path           TEXT NOT NULL CHECK (char_length(file_path) BETWEEN 1 AND 4096),
    start_line          INTEGER NULL CHECK (start_line IS NULL OR start_line >= 1),
    end_line            INTEGER NULL
                        CHECK (end_line IS NULL OR (start_line IS NOT NULL AND end_line >= start_line)),
    entity_id           BIGINT NULL,
    commit_sha          TEXT NULL
                        CHECK (commit_sha IS NULL OR commit_sha ~ '^[0-9a-f]{40}$'),
    excerpt_sha         TEXT NOT NULL DEFAULT ''
                        CHECK (excerpt_sha = '' OR excerpt_sha ~ '^sha256:[0-9a-f]{64}$'),
    status              TEXT NOT NULL DEFAULT 'valid'
                        CHECK (status IN ('valid', 'stale', 'broken')),
    confidence          TEXT NOT NULL DEFAULT 'high'
                        CHECK (confidence IN ('low', 'medium', 'high')),
    created_by_kind     TEXT NOT NULL DEFAULT 'user'
                        CHECK (created_by_kind IN ('user', 'ai', 'system')),
    created_by          BIGINT NULL,
    last_validated_at   TIMESTAMPTZ NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_code_anchor PRIMARY KEY (id)
);

-- Null line numbers are normalized to 0 for the point uniqueness key, so the
-- rule is expressed as a unique expression index (a UNIQUE table constraint
-- cannot carry COALESCE expressions).
CREATE UNIQUE INDEX uk_code_anchor_subject_point
    ON code_anchor (version_id, subject_kind, subject_id, file_path,
                    COALESCE(start_line, 0), COALESCE(end_line, 0));
CREATE INDEX idx_code_anchor_subject ON code_anchor (subject_kind, subject_id);
CREATE INDEX idx_code_anchor_repo_path ON code_anchor (repo_id, file_path);

CREATE TABLE code_fact_review (
    id              BIGINT NOT NULL,
    fact_id         BIGINT NOT NULL REFERENCES code_fact (id) ON DELETE CASCADE,
    fact_version    INTEGER NOT NULL CHECK (fact_version >= 1),
    repo_id         BIGINT NOT NULL,
    commit_sha      TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    verdict         TEXT NOT NULL CHECK (verdict IN ('correct', 'incorrect')),
    reason          TEXT NOT NULL DEFAULT '' CHECK (char_length(reason) <= 2000),
    reviewer_id     BIGINT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_code_fact_review PRIMARY KEY (id)
);

CREATE INDEX idx_code_fact_review_fact ON code_fact_review (fact_id, id DESC);
CREATE INDEX idx_code_fact_review_created ON code_fact_review (created_at DESC);
