-- v0.2.4-C skill library v1 (R-C10/R-C11): DB-authoritative runtime skill
-- store with append-only versions. skills/ files are only the first-start
-- seed source; administrator edits (including deletes) are never overwritten
-- by re-seeding. Skill bodies live here and in skills/ files, never in Go
-- source.

CREATE TABLE skill (
    id          BIGINT PRIMARY KEY,
    name        TEXT NOT NULL CHECK (name ~ '^[a-z][a-z0-9_-]{0,63}$'),
    description TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL CHECK (length(body) > 0),
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    version     INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    -- Soft delete: a deleted skill stays hidden from every read while its
    -- append-only history survives, and re-seeding never resurrects it.
    deleted_at  TIMESTAMPTZ NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One active skill per name; a deleted name may be re-created as a fresh
-- aggregate (new id, version restarts at 1).
CREATE UNIQUE INDEX uk_skill_name_active ON skill (name) WHERE deleted_at IS NULL;

CREATE TABLE skill_version (
    id          BIGINT PRIMARY KEY,
    skill_id    BIGINT NOT NULL REFERENCES skill (id),
    version     INTEGER NOT NULL CHECK (version >= 1),
    name        TEXT NOT NULL,
    description TEXT NOT NULL,
    body        TEXT NOT NULL CHECK (length(body) > 0),
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    enabled     BOOLEAN NOT NULL,
    source      TEXT NOT NULL CHECK (source IN ('seed', 'admin')),
    actor_id    BIGINT NULL,
    note        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_skill_version UNIQUE (skill_id, version)
);
CREATE INDEX idx_skill_version_skill ON skill_version (skill_id, version DESC);