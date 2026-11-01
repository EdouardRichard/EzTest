-- P5 agent context: extend work_item.team with the codeintel team (#31).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md §3.8.
-- Additive CHECK widening only; work_item.version_id stays NOT NULL because
-- codeintel tasks are version scoped (B1a). team_room_* team checks are not
-- changed: P5 does not create a code team room.
-- The old check was created inline on work_item.team, so PostgreSQL auto-named
-- it work_item_team_check; DROP IF EXISTS keeps this migration safe on a fresh DB.
-- New constraint name follows the P5 contract: chk_work_item_team.

ALTER TABLE work_item DROP CONSTRAINT IF EXISTS work_item_team_check;
ALTER TABLE work_item ADD CONSTRAINT chk_work_item_team
    CHECK (team IN ('clarification', 'design', 'execution', 'delivery', 'steward', 'code'));
