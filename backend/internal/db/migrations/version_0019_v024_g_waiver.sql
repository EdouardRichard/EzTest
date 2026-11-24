-- v0.2.4-G (T2, SC-G11 / GD-06 / P23): two-level admin waiver columns.
-- version.closure_kind classifies the close (normal | waived); version.unverified
-- marks a waived close whose report carries unverified acceptance criteria.
-- requirement.waived wires the requirement-level waiver flag as a real persisted
-- column (today always false; the G T6 waiver write path sets it through the
-- single Close terminal entry, and reopen clears it). Append-only (GD-05):
-- existing rows default to normal/false, no backfill.
--
-- The closure_kind CHECK enum is a persisted projection of the Go domain authority
-- version.ClosureKinds() (normal/waived); the single-source gate in
-- db/closure_kind_enum_test.go and the applied-constraint schema pin
-- (db.TestGMigrationSchema) derive their expected set from that authority, never
-- hand-copied (constitution 五.1).
ALTER TABLE version
    ADD COLUMN closure_kind TEXT NOT NULL DEFAULT 'normal'
        CONSTRAINT chk_version_closure_kind
        CHECK (closure_kind IN ('normal', 'waived')),
    ADD COLUMN unverified BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE requirement
    ADD COLUMN waived BOOLEAN NOT NULL DEFAULT false;