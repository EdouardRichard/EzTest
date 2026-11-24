-- v0.2.4-B resource context: invert the catalog hierarchy from
-- SUT -> environment -> assets to environment -> SUT -> assets (SC-B1, design
-- v1.3.1 §3.1/§3.2).
--
--   environment      becomes a TOP-LEVEL table (name/description/status); it no
--                    longer belongs to a SUT and no longer carries base_url
--   sut              gains environment_id (real FK, same context) and base_url;
--                    its name is unique per environment
--   sut_page/sut_api/target_database
--                    are owned by sut_id alone: the environment_id double FK is
--                    dropped and every unique constraint is re-scoped to sut_id
--   test_account     keeps the v0.22 global pool (DB-15) and gains a nullable
--                    sut_id AFFINITY column only; the affinity behaviour belongs
--                    to a later subproject, no filtering is implied here
--   sut_repository   gains code_baseline_commit, the new home of the
--                    per-(SUT, repository) code baseline that used to live on
--                    version_sut (dropped by version_0015). Same 40-hex CHECK as
--                    version_0006_version_sut_code_baseline.sql.
--
-- GD-05 (destructive): the previous catalog rows belong to the inverted
-- hierarchy and have no environment to join, so old data is discardable. No
-- backfill and no dual-track compatibility layer. Append-only: this file
-- supersedes resource_0001/0002/0003 and the sut_repository shape introduced by
-- version_0010 instead of editing them.

-- 1. The new top-level environment.
CREATE TABLE environment (
    id          BIGINT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_environment PRIMARY KEY (id)
);

CREATE UNIQUE INDEX uk_environment_name ON environment (lower(name));

-- 2. GD-05: discard the old catalog before the SUT ownership column becomes
-- mandatory. sut_page/sut_api/target_database cascade from sut; the
-- sut_repository associations are logical cross-table rows without an FK, so
-- they are cleared explicitly to avoid orphans.
DELETE FROM sut_repository;
DELETE FROM sut;

-- 3. Assets: drop the environment_id double FK and re-scope uniqueness to the
-- owning SUT. This has to happen before sut_environment can be dropped, because
-- these columns are what still depend on it. The indexes are dropped explicitly
-- first so the intent is clear; DROP COLUMN would remove them implicitly.
DROP INDEX IF EXISTS uk_sut_page_normalized_url;
ALTER TABLE sut_page DROP COLUMN environment_id;
CREATE UNIQUE INDEX uk_sut_page_normalized_url ON sut_page (sut_id, normalized_url);

DROP INDEX IF EXISTS uk_sut_api_route;
ALTER TABLE sut_api DROP COLUMN environment_id;
CREATE UNIQUE INDEX uk_sut_api_route ON sut_api (sut_id, method, path_template);

DROP INDEX IF EXISTS uk_target_database_name;
ALTER TABLE target_database DROP COLUMN environment_id;
CREATE UNIQUE INDEX uk_target_database_name ON target_database (sut_id, lower(name));

-- idx_sut_page_sut / idx_sut_api_sut (resource_0001) are now covered by the
-- leading sut_id column of the re-scoped unique indexes.
DROP INDEX IF EXISTS idx_sut_page_sut;
DROP INDEX IF EXISTS idx_sut_api_sut;

-- 4. The derived environment table of the old hierarchy is gone.
DROP TABLE IF EXISTS sut_environment;

-- 5. sut belongs to an environment and owns base_url.
ALTER TABLE sut
    ADD COLUMN environment_id BIGINT NOT NULL
        CONSTRAINT fk_sut_environment REFERENCES environment (id) ON DELETE CASCADE,
    ADD COLUMN base_url TEXT NOT NULL;

CREATE INDEX idx_sut_environment ON sut (environment_id);

-- The v0.2 P0 global name uniqueness (resource_0001) is re-scoped to the
-- environment: the same SUT name may exist in two environments.
DROP INDEX IF EXISTS uk_sut_name;
CREATE UNIQUE INDEX uk_sut_name ON sut (environment_id, lower(name));

-- 6. test_account: nullable SUT affinity on top of the global pool. Deleting a
-- SUT clears the affinity instead of deleting a shared credential.
ALTER TABLE test_account
    ADD COLUMN sut_id BIGINT NULL
        CONSTRAINT fk_test_account_sut REFERENCES sut (id) ON DELETE SET NULL;

CREATE INDEX idx_test_account_sut ON test_account (sut_id) WHERE sut_id IS NOT NULL;

-- 7. sut_repository: the per-(SUT, repository) code baseline. NULL means
-- "unresolved"; a present value is a lowercase 40-hex Git commit SHA, matching
-- the constraint version_0006 put on the dropped version_sut column.
ALTER TABLE sut_repository
    ADD COLUMN code_baseline_commit TEXT NULL
        CONSTRAINT chk_sut_repository_code_baseline_commit
        CHECK (code_baseline_commit IS NULL OR code_baseline_commit ~ '^[0-9a-f]{40}$');
