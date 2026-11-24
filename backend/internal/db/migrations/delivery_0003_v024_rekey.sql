-- v0.2.4-B (T14): mechanical rekey of the delivery issue/report references from
-- the dropped version_sut binding-row id to the SUT id.
--
-- W2 (version_0015) dropped the version_sut chain: there is no binding-row id
-- anymore, so delivery.VersionSUTSnapshot.ID now equals .SUTID. This migration
-- renames the persisted column and the report-ref kind label to match the SUT id
-- domain the Go types already carry. It is a MECHANICAL rekey only: the id values
-- are unchanged, and the P22 report-input fingerprint REDEFINITION (conclusion
-- inputs, excluding updated_at / terminal_cause) belongs to subproject G, not
-- here. Nothing about which rows participate changes.
--
-- Four-site lockstep contract for the report-ref kind: the literal lives in
--   1. delivery emit     -- application/report_service.go reportRefsForSnapshot
--   2. delivery validate -- infrastructure/postgres/report_repo.go validReportRefKind
--   3. this DB CHECK     -- delivery_report_ref_kind_check below
--   4. chain read        -- chain/infrastructure/providers/delivery.go reportRefKindSUT
-- All four MUST say 'sut' together; a drift silently drops a report node's SUT ids
-- (the chain providers_integration_test.go report-ref guard pins this).

-- 1. issue ref column: version_sut_id -> sut_id (nullable BIGINT, logical id).
ALTER TABLE delivery_issue_ref RENAME COLUMN version_sut_id TO sut_id;

-- 2. report ref kind: replace 'version_sut' with 'sut', preserving every other
--    allowed kind. Existing rows are relabelled first so the tightened CHECK
--    cannot reject a populated table; the id domain (ref_id) is untouched.
UPDATE delivery_report_ref SET kind = 'sut' WHERE kind = 'version_sut';

ALTER TABLE delivery_report_ref DROP CONSTRAINT delivery_report_ref_kind_check;
ALTER TABLE delivery_report_ref ADD CONSTRAINT delivery_report_ref_kind_check
    CHECK (kind IN ('requirement', 'scenario', 'case_version', 'wave',
                    'case_run', 'issue', 'sut'));