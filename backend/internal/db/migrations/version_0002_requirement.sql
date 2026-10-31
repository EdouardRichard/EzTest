-- P1 version context: immutable requirement versions, confirmations and relations.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md §3.4.

CREATE TABLE requirement (
    id                    BIGINT PRIMARY KEY,
    version_id            BIGINT NOT NULL REFERENCES version (id),
    current_version_id    BIGINT NULL,
    status                TEXT NOT NULL DEFAULT 'draft'
                          CHECK (status IN ('draft', 'clarified', 'closed', 'obsolete')),
    target_version_sut_ids JSONB NOT NULL DEFAULT '[]'::jsonb
                          CHECK (jsonb_typeof(target_version_sut_ids) = 'array'),
    closed_at             TIMESTAMPTZ NULL,
    closed_by_run_id      BIGINT NULL,
    created_by            BIGINT NULL,
    created_by_run_id     BIGINT NULL,
    source_conversation_id BIGINT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_requirement_version_status ON requirement (version_id, status, id DESC);

CREATE TABLE requirement_version (
    id                  BIGINT PRIMARY KEY,
    requirement_id      BIGINT NOT NULL REFERENCES requirement (id),
    revision            INTEGER NOT NULL CHECK (revision > 0),
    title               TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    priority            TEXT NOT NULL DEFAULT 'P2' CHECK (priority IN ('P0', 'P1', 'P2', 'P3')),
    constraints         JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(constraints) = 'object'),
    target_version_suts JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_version_suts) = 'array'),
    target_pages        JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_pages) = 'array'),
    target_apis         JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_apis) = 'array'),
    content_hash        TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    created_by          BIGINT NULL,
    created_by_run_id   BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (requirement_id, revision)
);

CREATE INDEX idx_requirement_version_latest ON requirement_version (requirement_id, id DESC);

CREATE TABLE requirement_confirmation (
    id                      BIGINT PRIMARY KEY,
    requirement_version_id  BIGINT NOT NULL REFERENCES requirement_version (id),
    scope                   TEXT NOT NULL CHECK (scope IN ('requirement', 'batch')),
    batch_id                TEXT NULL,
    actor_id                BIGINT NOT NULL,
    confirmed_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    affected_requirement_ids JSONB NOT NULL DEFAULT '[]'::jsonb
                             CHECK (jsonb_typeof(affected_requirement_ids) = 'array'),
    UNIQUE (requirement_version_id)
);

CREATE INDEX idx_requirement_confirmation_batch ON requirement_confirmation (batch_id);
CREATE INDEX idx_requirement_confirmation_actor ON requirement_confirmation (actor_id, confirmed_at DESC);

CREATE TABLE requirement_relation (
    id                    BIGINT PRIMARY KEY,
    version_id            BIGINT NOT NULL,
    from_requirement_id   BIGINT NOT NULL,
    to_requirement_id     BIGINT NOT NULL,
    kind                  TEXT NOT NULL
                          CHECK (kind IN ('depends_on', 'conflicts_with', 'overlaps', 'shares_scope')),
    description           TEXT NOT NULL DEFAULT '',
    source_revision_id    BIGINT NULL,
    created_by            BIGINT NULL,
    created_by_run_id     BIGINT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_requirement_id <> to_requirement_id),
    UNIQUE (version_id, from_requirement_id, to_requirement_id, kind)
);

CREATE INDEX idx_requirement_relation_from ON requirement_relation (version_id, from_requirement_id);
CREATE INDEX idx_requirement_relation_to ON requirement_relation (version_id, to_requirement_id);
