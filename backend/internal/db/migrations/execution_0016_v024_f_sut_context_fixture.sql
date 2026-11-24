-- v0.2.4-F execution context (Task 13, SC-F5 / P18): the session fixture REUSE
-- LINK. case_run_sut_context records which append-only session_fixture fact a
-- (SUT, account_role) executor session saved or injected, so every cross-case
-- login-state reuse is traceable from the run to the exact fixture version.
--
-- Nullable: a context that never saved/loaded a fixture has no reference. The
-- reference is a REAL within-context foreign key (both tables live in the
-- execution context); session_fixture rows are append-only and never deleted,
-- so the FK never cascades. Cross-context references on session_fixture itself
-- (version/sut/environment) stay logical BIGINT (execution_0013).
--
-- Append-only migration: supersedes nothing, adds one nullable column. No
-- destructive change, no backfill.

ALTER TABLE case_run_sut_context
    ADD COLUMN session_fixture_id BIGINT NULL
        CONSTRAINT fk_case_run_sut_context_fixture REFERENCES session_fixture (id);