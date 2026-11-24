-- v0.2.4-B resource context: reserve the SUT registration schema (R-B4, owner
-- approved 2026-11-14, option b). Subproject B wires the ADMIN creation path
-- (source=admin, status=approved); the Agent-facing sut.register tool, the
-- evidence gate and the parent-path heuristic that write source=ai/status=proposed
-- belong to subproject F and are NOT built here.
--
--   sut              gains source (admin/ai/import) and status
--                    (proposed/approved/rejected). This is the SUT's OWN
--                    registration vocabulary, NOT the shared active/disabled
--                    lifecycle used by environment/target_database/test_account/
--                    code_repository. Existing rows take admin/approved: every
--                    SUT today was created by an admin, so the default is
--                    semantically correct. Only status=approved SUTs enter a
--                    version's effective scope (resourceapp.SUTView.EffectiveAssets
--                    / versionapp.EnvironmentSUTCatalog.EffectiveSUTs).
--   sut_registration a NEW append-only fact table recording how each SUT came to
--                    be registered. The repository exposes only Append/List (no
--                    Update/Delete); registering the same SUT twice appends a
--                    second row and never rewrites the first. It has a production
--                    reader (GET /api/resources/suts/{id}/registrations) so it is
--                    not a write-only table (violations.md V-0019).
--
-- Append-only: this file supersedes nothing; it adds columns and a table on top of
-- resource_0004. No destructive change, no backfill beyond the column defaults.

-- 1. SUT registration source and status.
ALTER TABLE sut
    ADD COLUMN source TEXT NOT NULL DEFAULT 'admin'
        CONSTRAINT chk_sut_source CHECK (source IN ('admin', 'ai', 'import')),
    ADD COLUMN status TEXT NOT NULL DEFAULT 'approved'
        CONSTRAINT chk_sut_status CHECK (status IN ('proposed', 'approved', 'rejected'));

-- 2. The append-only registration fact table.
CREATE TABLE sut_registration (
    id                BIGINT NOT NULL,
    sut_id            BIGINT NOT NULL
        CONSTRAINT fk_sut_registration_sut REFERENCES sut (id) ON DELETE CASCADE,
    source            TEXT NOT NULL DEFAULT 'admin'
        CONSTRAINT chk_sut_registration_source CHECK (source IN ('admin', 'ai', 'import')),
    evidence_refs     JSONB NOT NULL DEFAULT '[]'::jsonb,
    resolution_method TEXT NOT NULL DEFAULT '',
    gate_result       TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by        BIGINT NOT NULL,
    CONSTRAINT pk_sut_registration PRIMARY KEY (id)
);

-- Per-SUT time-ordered read path (GET /api/resources/suts/{id}/registrations).
CREATE INDEX idx_sut_registration_sut ON sut_registration (sut_id, created_at, id);
