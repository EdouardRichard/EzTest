-- v0.2.4-B version context: a version binds an environment directly (SC-B2,
-- design v1.3.1 §3.2/§3.3). The whole derived binding chain is dropped:
--
--   version_sut                  the single environment-derived SUT binding,
--                                including its scope_exclude document and the
--                                code_baseline_commit column (the baseline moved
--                                to sut_repository in
--                                resource_0004_v024_hierarchy_inversion.sql)
--   version_sut_resource         V-0019: deleted, no replacement
--   version_repository_override  the environment code pool is now the derived
--                                union of the environment's SUT repositories
--                                (GD-03), read through the resource catalog
--
-- What survives: version_account (the version still pins its own account
-- subset) and the stage machine / close projection on version.
--
-- version.environment_id is a CROSS-CONTEXT reference into the resource context
-- and therefore stays a logical BIGINT with no physical foreign key
-- (Agent.md §6.9.7, cf. version_0010_v022_binding_handoff.sql which keeps
-- sut_id/environment_id/repo_id/account_id logical for the same reason). The
-- resource delete guard (version.Repository.EnvironmentInUse) is the
-- application-level protection against deleting a bound environment.
--
-- GD-05 (destructive): old versions are discardable, there is no backfill from
-- version_sut and no dual-track compatibility layer. The column is nullable at
-- the DB level only because an ALTER on a populated table cannot introduce a
-- mandatory value; the version domain (Version.Validate) and the create use
-- case both require a positive environment_id, so every row written by this
-- release carries one. Append-only: this file supersedes version_0001/0006/0010
-- instead of editing them.

ALTER TABLE version ADD COLUMN environment_id BIGINT NULL;

CREATE INDEX idx_version_environment ON version (environment_id) WHERE environment_id IS NOT NULL;

-- Drop order respects the version_sut_resource -> version_sut foreign key.
DROP TABLE IF EXISTS version_sut_resource;
DROP TABLE IF EXISTS version_repository_override;
DROP TABLE IF EXISTS version_sut;
