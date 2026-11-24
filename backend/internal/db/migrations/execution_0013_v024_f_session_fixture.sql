-- v0.2.4-F execution context (Task 2, SC-F5 / P18): the append-only session
-- fixture fact. One row records a login storage-state the execution team saved
-- for reuse across case_runs, keyed by (version, sut, environment, role) — the
-- cross-case reuse layer above today's per-run storage_state. storage_state_ref
-- is the content-addressed reference to the saved state in the content ledger;
-- the bytes never live here.
--
-- Append-only (version_fact / sut_registration precedent): rows are only ever
-- inserted, never rewritten or deleted; the repository exposes Append/List only.
-- created_at and NO updated_at. Saving the same reuse key again appends a second
-- row (latest wins), so the (version,sut,environment,role) key is a LOOKUP index,
-- deliberately NOT a unique constraint.
--
-- References are logical BIGINT (cross-context: version_id -> version,
-- sut_id -> sut, environment_id -> environment), no physical foreign key
-- (constitution 五.7). role is the account role and storage_state_ref the content
-- reference; both must be non-empty.
--
-- Append-only migration: supersedes nothing, adds one table. No destructive
-- change, no backfill.

CREATE TABLE session_fixture (
    id                BIGINT NOT NULL,
    version_id        BIGINT NOT NULL,
    sut_id            BIGINT NOT NULL,
    environment_id    BIGINT NOT NULL,
    role              TEXT NOT NULL
        CONSTRAINT chk_session_fixture_role CHECK (role <> ''),
    storage_state_ref TEXT NOT NULL
        CONSTRAINT chk_session_fixture_storage_state_ref CHECK (storage_state_ref <> ''),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by        BIGINT NOT NULL,
    CONSTRAINT pk_session_fixture PRIMARY KEY (id)
);

-- Reuse-key lookup: the latest fixture of one (version,sut,environment,role).
CREATE INDEX idx_session_fixture_key
    ON session_fixture (version_id, sut_id, environment_id, role, id);