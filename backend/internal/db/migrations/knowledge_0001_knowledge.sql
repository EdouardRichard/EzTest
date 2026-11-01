-- P7 knowledge context: cross-version knowledge items, their append-only
-- content revisions and injection usage rows (#34).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md §5 (#34).
-- IDs are application-generated snowflakes; version/agent_run/work_item
-- references are logical BIGINT only (no cross-context FK). Full-text search
-- uses a stored generated tsvector ('simple' configuration) + GIN; no vector
-- RAG in 0.2. knowledge_revision is append-only: updated_at is initialized on
-- insert and never updated by the repository.
--
-- PostgreSQL does not mark array_to_string() IMMUTABLE (its result can depend on
-- element output functions), so a stored generated column cannot call it
-- directly. Tags are TEXT[]; this wrapper is genuinely immutable and keeps the
-- spec's search vector expression (title + content + tags).
CREATE FUNCTION eztest_text_array_to_string(items TEXT[]) RETURNS TEXT
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$ SELECT coalesce(array_to_string(items, ' '), '') $$;

CREATE TABLE knowledge_item (
    id                       BIGINT NOT NULL,
    scope                    TEXT NOT NULL CHECK (scope IN ('global', 'system')),
    system_key               TEXT NULL
                             CHECK (system_key IS NULL OR (system_key = lower(system_key)
                                   AND char_length(system_key) BETWEEN 1 AND 64)),
    title                    TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    content                  TEXT NOT NULL CHECK (char_length(content) BETWEEN 1 AND 20000),
    tags                     TEXT[] NOT NULL DEFAULT '{}'::text[] CHECK (cardinality(tags) <= 20),
    status                   TEXT NOT NULL DEFAULT 'draft'
                             CHECK (status IN ('draft', 'approved', 'rejected', 'revoked')),
    confidence               NUMERIC(4,3) NULL
                             CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    source_version_id        BIGINT NULL,
    promoted_from_version_id BIGINT NULL,
    revision                 INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    review_reason            TEXT NULL CHECK (review_reason IS NULL OR char_length(review_reason) <= 2000),
    reviewed_by              BIGINT NULL,
    reviewed_at              TIMESTAMPTZ NULL,
    revoked_at               TIMESTAMPTZ NULL,
    created_by               BIGINT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    search_vector            tsvector GENERATED ALWAYS AS (
        to_tsvector('simple',
            coalesce(title, '') || ' ' || coalesce(content, '') || ' ' || eztest_text_array_to_string(tags))
    ) STORED,
    CONSTRAINT pk_knowledge_item PRIMARY KEY (id),
    CONSTRAINT ck_knowledge_item_scope CHECK (
        (scope = 'global' AND system_key IS NULL) OR
        (scope = 'system' AND system_key IS NOT NULL)
    )
);

CREATE INDEX idx_knowledge_item_scope_status_id ON knowledge_item (scope, status, id DESC);
CREATE INDEX idx_knowledge_item_status_id ON knowledge_item (status, id DESC);
CREATE INDEX idx_knowledge_item_source_version
    ON knowledge_item (source_version_id) WHERE source_version_id IS NOT NULL;
CREATE INDEX idx_knowledge_item_search ON knowledge_item USING GIN (search_vector);

CREATE TABLE knowledge_revision (
    id           BIGINT NOT NULL,
    knowledge_id BIGINT NOT NULL REFERENCES knowledge_item (id) ON DELETE CASCADE,
    revision     INTEGER NOT NULL CHECK (revision > 0),
    title        TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    content      TEXT NOT NULL CHECK (char_length(content) BETWEEN 1 AND 20000),
    tags         TEXT[] NOT NULL DEFAULT '{}'::text[] CHECK (cardinality(tags) <= 20),
    changed_by   BIGINT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_knowledge_revision PRIMARY KEY (id),
    CONSTRAINT uk_knowledge_revision_knowledge_id_revision UNIQUE (knowledge_id, revision)
);

CREATE INDEX idx_knowledge_revision_knowledge_id_id
    ON knowledge_revision (knowledge_id, id DESC);

CREATE TABLE knowledge_usage (
    id             BIGINT NOT NULL,
    knowledge_id   BIGINT NOT NULL REFERENCES knowledge_item (id) ON DELETE CASCADE,
    agent_run_id   BIGINT NULL,
    work_item_id   BIGINT NULL,
    token_estimate INTEGER NOT NULL DEFAULT 0 CHECK (token_estimate >= 0),
    injected_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_knowledge_usage PRIMARY KEY (id)
);

CREATE INDEX idx_knowledge_usage_knowledge_id_id ON knowledge_usage (knowledge_id, id DESC);
