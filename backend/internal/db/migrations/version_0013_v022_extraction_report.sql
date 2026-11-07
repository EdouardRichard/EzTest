-- v0.22 A-7 reconciliation report (R-A7-8, docs/version-V0.22/A §7.7, Lead
-- ruling C12): persist the full declarative-vs-extracted report on the run so
-- the progress API/UI can show "58/58 current + 32 planned" without reparsing
-- the document. The V1 current and V2 planned partitions are separate:
-- expected/extracted/missing_ids/low_confidence/skipped_images plus
-- planned_count/planned_ids live in this JSON object. Additive column; old rows
-- read as an empty object and Reconcile fills it.
--
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md DB-7 and
-- docs/version-V0.22/A-model-token-budget.md §7.7.

ALTER TABLE requirement_extraction_run
    ADD COLUMN report JSONB NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT chk_requirement_extraction_run_report
        CHECK (jsonb_typeof(report) = 'object');
