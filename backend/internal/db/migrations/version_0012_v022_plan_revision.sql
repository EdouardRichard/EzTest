-- v0.22 A-8 plan write path: optimistic-lock revision for version_plan_item.
-- R-A8-2 requires plan writes (Host plan.update and PATCH
-- /api/versions/{id}/plan-items/{itemId}) to carry a revision, but the 0.2.1
-- table (version_0007) has no revision column. One additive column provides it;
-- no plan row is rewritten (v0.22 starts from a fresh database), so existing
-- rows start at revision 1 and every real write bumps it.
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §4.8 R-A8-2,
-- §5 DB-20 and docs/version-V0.22/B-clarification-messaging.md §9.1.

ALTER TABLE version_plan_item
    ADD COLUMN revision INTEGER NOT NULL DEFAULT 1
        CONSTRAINT chk_version_plan_item_revision CHECK (revision > 0);
