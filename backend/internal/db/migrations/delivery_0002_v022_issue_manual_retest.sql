-- v0.22 W2-4: manual issue source and retest result model (DB-23).
-- Additive only: the P4 issue status machine (open/confirmed/dismissed/merged)
-- is unchanged. Retest columns are written by W2-5.
--
-- source gains 'manual' so POST/PATCH /api/versions/{id}/issues can persist
-- manually created/edited issues next to failure candidates.
ALTER TABLE delivery_issue
    DROP CONSTRAINT IF EXISTS delivery_issue_source_check;

ALTER TABLE delivery_issue
    ADD CONSTRAINT ck_delivery_issue_source
        CHECK (source IN ('failure_candidate', 'manual'));

-- retest_status is independent of the issue lifecycle status; the last retest
-- wave/case_run pointers record the most recent retest attempt.
ALTER TABLE delivery_issue
    ADD COLUMN retest_status TEXT NOT NULL DEFAULT 'not_retested'
        CHECK (retest_status IN ('not_retested', 'retesting', 'passed', 'failed')),
    ADD COLUMN last_retest_wave_id BIGINT NULL,
    ADD COLUMN last_retest_case_run_id BIGINT NULL;
