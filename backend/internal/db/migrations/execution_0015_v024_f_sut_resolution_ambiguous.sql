-- v0.2.4-F execution context (Task 9 fix round 1, controller ruling P19, SC-F2):
-- record the ambiguity flag on the append-only sut_resolution fact. On ambiguity
-- the P16 resolution SELECTS the top-ranked SUT (design §6.2 "歧义按上下文选择"
-- means select, not union); the touched assets are the SELECTED SUT's validated
-- set only. The flag lets downstream (the SC-F10 knowledge-dimension projection)
-- distinguish a selection-from-ambiguity from an unambiguous resolution.
--
-- Append-only migration: adds one NOT NULL column with a false default (existing
-- rows backfill to non-ambiguous, the honest legacy reading). Supersedes nothing,
-- no destructive change, no data rewrite. The fact stays append-only (Append/List
-- repository; created_at, no updated_at).

ALTER TABLE sut_resolution
    ADD COLUMN ambiguous BOOLEAN NOT NULL DEFAULT false;