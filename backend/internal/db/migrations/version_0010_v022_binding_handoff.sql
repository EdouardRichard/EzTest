-- v0.22 C resources/binding schema: version description/goal, environment-level
-- exclusion scopes, SUT multi-repository bindings, per-version repository
-- overrides, the global test-account pool with per-version enablement, and the
-- clarification-to-design handoff artifact.
--
-- Destructive rebuild of the 0.21 shape (v0.22 starts from a fresh database):
-- version_sut loses its single-repository column and test_account becomes a
-- global pool. Cross-context references (sut_id/repo_id/version_id/account_id)
-- stay logical BIGINT; foreign keys are only used inside the version context
-- (Agent.md §6.9.7).
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-10..DB-15,
-- DB-22, docs/version-V0.22/C-version-sut-resources.md §3 and
-- docs/version-V0.22/B-clarification-messaging.md §8.1.

-- DB-10: version description and goal. version.blocker already exists (0.2.1,
-- TEXT) and stays untouched: W0 keeps TEXT and stores structured JSON text
-- (lead ruling C6), so only the two new columns land here.
ALTER TABLE version
    ADD COLUMN description TEXT NOT NULL DEFAULT '',
    ADD COLUMN goal TEXT NOT NULL DEFAULT '';

-- DB-11: environment-level resource exclusions. The 0.21 scope column already
-- held a JSONB object; rename it in place so existing rows keep their content,
-- then pin the v0.22 default (three empty arrays = whole environment) and the
-- object shape. Pages/APIs/databases only; accounts and repositories have
-- their own version-level tables below.
ALTER TABLE version_sut RENAME COLUMN scope TO scope_exclude;

ALTER TABLE version_sut
    ALTER COLUMN scope_exclude SET DEFAULT '{"pages":[],"apis":[],"databases":[]}'::jsonb,
    ADD CONSTRAINT chk_version_sut_scope_exclude
        CHECK (jsonb_typeof(scope_exclude) = 'object');

-- DB-11: the single-repository column moves to sut_repository (DB-12). Drop its
-- index first so the intent is explicit, then the column; the
-- uk_version_sut (version_id, sut_id) natural key, environment_id and the
-- P5 code_baseline_commit stay.
DROP INDEX idx_version_sut_repo;

ALTER TABLE version_sut DROP COLUMN code_repo_id;

-- DB-12: one SUT may associate two or more code repositories (e.g. backend and
-- frontend). A version inherits them unless overridden in
-- version_repository_override.
CREATE TABLE sut_repository (
    id          BIGINT NOT NULL,
    sut_id      BIGINT NOT NULL,
    repo_id     BIGINT NOT NULL,
    role        TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_sut_repository PRIMARY KEY (id),
    CONSTRAINT uk_sut_repository_sut_repo UNIQUE (sut_id, repo_id)
);

CREATE INDEX idx_sut_repository_repo ON sut_repository (repo_id);

-- DB-13: version-level repository append/exclude on top of the SUT bindings.
CREATE TABLE version_repository_override (
    id          BIGINT NOT NULL,
    version_id  BIGINT NOT NULL,
    repo_id     BIGINT NOT NULL,
    included    BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_version_repository_override PRIMARY KEY (id),
    CONSTRAINT uk_version_repository_override_version_repo UNIQUE (version_id, repo_id)
);

-- DB-14: the accounts a version may execute with. Execution only accepts
-- account_id values bound here with enabled = true.
CREATE TABLE version_account (
    id          BIGINT NOT NULL,
    version_id  BIGINT NOT NULL,
    account_id  BIGINT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_version_account PRIMARY KEY (id),
    CONSTRAINT uk_version_account_version_account UNIQUE (version_id, account_id)
);

CREATE INDEX idx_version_account_account ON version_account (account_id);

-- DB-15: test accounts become a global pool. The old uniqueness keyed on the
-- environment_id that is being removed, so replace it with a global
-- case-insensitive unique index (lead ruling C8). roles/status/password/notes
-- and the timestamps stay.
DROP INDEX uk_test_account_username;

ALTER TABLE test_account
    DROP COLUMN sut_id,
    DROP COLUMN environment_id;

CREATE UNIQUE INDEX uk_test_account_username ON test_account (lower(username));

-- DB-22: the clarification -> design handoff pack. One row per revision;
-- version_id is a logical version-context reference (no cross-context FK,
-- Agent.md §6.9.7). A regenerate writes the next revision.
CREATE TABLE clarification_handoff (
    id          BIGINT NOT NULL,
    version_id  BIGINT NOT NULL,
    revision    INTEGER NOT NULL
                CONSTRAINT chk_clarification_handoff_revision CHECK (revision > 0),
    content     JSONB NOT NULL DEFAULT '{}'::jsonb
                CONSTRAINT chk_clarification_handoff_content
                CHECK (jsonb_typeof(content) = 'object'),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_clarification_handoff PRIMARY KEY (id),
    CONSTRAINT uk_clarification_handoff_version_revision UNIQUE (version_id, revision)
);
