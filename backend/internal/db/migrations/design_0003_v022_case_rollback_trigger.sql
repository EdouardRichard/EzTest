-- v0.22 WP-6 rollback trigger (spec R-EX-6 / DB-30 / API-41): the rollback
-- replacement revision is a new confirmed case_version with trigger='rollback'
-- and rollback_of=<auto-confirmed revision>; the corresponding
-- case_confirmation audit row carries the same trigger. Additive only: widen
-- the two CHECK constraints delivered by design_0002, no column/table change
-- and no case_revision table (DB-28).
--
-- Existing rows remain valid ('normal'/'discovery_delta' and
-- 'manual'/'discovery_delta'); only the new enum value is added.

ALTER TABLE case_version
    DROP CONSTRAINT chk_case_version_trigger;
ALTER TABLE case_version
    ADD CONSTRAINT chk_case_version_trigger
    CHECK (trigger IN ('normal', 'discovery_delta', 'rollback'));

ALTER TABLE case_confirmation
    DROP CONSTRAINT chk_case_confirmation_trigger;
ALTER TABLE case_confirmation
    ADD CONSTRAINT chk_case_confirmation_trigger
    CHECK (trigger IN ('manual', 'discovery_delta', 'rollback'));
