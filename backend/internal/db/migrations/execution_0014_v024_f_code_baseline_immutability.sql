-- v0.2.4-F (Task 2, SC-F7 / V-0024): DB-level immutability guard for the
-- per-(SUT, repository) code baseline. The column lives on sut_repository
-- (resource context, added by resource_0004 with the 40-hex CHECK
-- chk_sut_repository_code_baseline_commit). Until now its only SQL writer
-- (resource Repository.SetSUTRepositoryBaselineCommit) was an unconditional
-- UPDATE whose NULLIF($3,'') could clear an already-pinned commit, and any other
-- writer could overwrite it — "pinned forever" was only an application-layer
-- check-then-write, not a database invariant.
--
-- This migration adds a BEFORE UPDATE row trigger so a PINNED (non-null) commit
-- can be neither overwritten with a different value nor cleared back to NULL,
-- regardless of which SQL path writes it. The first pin (NULL -> SHA) and a
-- no-op re-pin of the identical SHA stay allowed. The original 40-hex CHECK is
-- left untouched: a malformed SHA is still rejected on an unpinned row.
--
-- A trigger (rather than rewriting one writer's UPDATE ... WHERE IS NULL) guards
-- EVERY current and future write path, which is the registered V-0024 verification
-- method. The guard is named functionally; it carries no stage codename.
--
-- Append-only migration: supersedes nothing, adds one function and one trigger on
-- the existing resource table. No column/table change, no destructive change, no
-- backfill. Although prefixed execution_ (it is delivered by the F execution
-- autonomy subproject), it guards the resource sut_repository table.

CREATE FUNCTION guard_sut_repository_code_baseline() RETURNS trigger AS $$
BEGIN
    IF OLD.code_baseline_commit IS NOT NULL
       AND NEW.code_baseline_commit IS DISTINCT FROM OLD.code_baseline_commit THEN
        RAISE EXCEPTION 'sut_repository.code_baseline_commit is pinned and immutable (id=%)', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sut_repository_code_baseline_immutable
    BEFORE UPDATE ON sut_repository
    FOR EACH ROW
    EXECUTE FUNCTION guard_sut_repository_code_baseline();