-- v0.22 EX discovery-delta case auto-confirmation (spec DB-30, §4.15 R-B7-1,
-- §4.31 R-EX-6): case versions record whether they were produced by a
-- discovery delta, which observation triggered them and which version they
-- roll back to; confirmations keep the same audit trail for automatic
-- confirmation. case_review is a stage value, not a table, and no
-- case_revision/finding tables are introduced.
--
-- source_observation_id/rollback_of are logical BIGINT references into the
-- execution/case contexts (Agent.md §6.9.7); no cross-context foreign keys.
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-30,
-- §4.15/§4.21 and docs/version-V0.22/C-version-sut-resources.md §5.

ALTER TABLE case_version
    ADD COLUMN trigger TEXT NOT NULL DEFAULT 'normal'
        CONSTRAINT chk_case_version_trigger
        CHECK (trigger IN ('normal', 'discovery_delta')),
    ADD COLUMN source_observation_id BIGINT NULL,
    ADD COLUMN auto_confirmed_by TEXT NULL,
    ADD COLUMN rollback_of BIGINT NULL;

CREATE INDEX idx_case_version_source_observation
    ON case_version (source_observation_id);

ALTER TABLE case_confirmation
    ADD COLUMN auto_confirmed BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN auto_confirmed_by TEXT NULL,
    ADD COLUMN trigger TEXT NOT NULL DEFAULT 'manual'
        CONSTRAINT chk_case_confirmation_trigger
        CHECK (trigger IN ('manual', 'discovery_delta')),
    ADD COLUMN source_observation_id BIGINT NULL;
