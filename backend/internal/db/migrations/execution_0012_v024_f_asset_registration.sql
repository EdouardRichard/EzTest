-- v0.2.4-F execution context (Task 2, SC-F3 / P17): the append-only asset
-- registration fact. One row records an asset (page/api/db) the execution team
-- explored, proved and registered for a SUT, together with the content-addressed
-- evidence the registration relied on and the server gate outcome. The evidence
-- gate (evidence exists + sha256 + linked step assertion passed) is enforced by
-- the P17 registration service before it appends an approved fact; an unproven
-- discovery is never registered.
--
-- Append-only (version_fact / sut_registration precedent): rows are only ever
-- inserted, never rewritten or deleted; the repository exposes Append/List only.
-- created_at and NO updated_at. Registering the same asset again (new evidence,
-- new gate outcome) appends a second row, so there is NO unique owner key.
--
-- References are logical BIGINT (cross-context into resource: sut_id -> sut,
-- asset_id -> sut_page/sut_api/target_database), no physical foreign key
-- (constitution 五.7). asset_kind is the registered asset vocabulary; source
-- mirrors sut_registration.source (admin/ai/import); evidence_refs is a JSONB
-- array of content-addressed evidence references (same shape as
-- sut_registration.evidence_refs); gate_result is the free-text gate outcome
-- (approved/rejected/...), owned by the P17 service.
--
-- Append-only migration: supersedes nothing, adds one table. No destructive
-- change, no backfill.

CREATE TABLE asset_registration (
    id            BIGINT NOT NULL,
    sut_id        BIGINT NOT NULL,
    asset_kind    TEXT NOT NULL
        CONSTRAINT chk_asset_registration_asset_kind CHECK (asset_kind IN ('page', 'api', 'db')),
    asset_id      BIGINT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'ai'
        CONSTRAINT chk_asset_registration_source CHECK (source IN ('admin', 'ai', 'import')),
    evidence_refs JSONB NOT NULL DEFAULT '[]'::jsonb
        CONSTRAINT chk_asset_registration_evidence_refs CHECK (jsonb_typeof(evidence_refs) = 'array'),
    gate_result   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by    BIGINT NOT NULL,
    CONSTRAINT pk_asset_registration PRIMARY KEY (id)
);

-- Per-SUT read path and per-asset lookup (knowledge dimension / catalog enrich).
CREATE INDEX idx_asset_registration_sut ON asset_registration (sut_id, id);
CREATE INDEX idx_asset_registration_asset ON asset_registration (asset_kind, asset_id);