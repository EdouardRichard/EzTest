-- P7 agent context: extend work_item.team with the deterministic worker team
-- (#40). P7's report.render_pdf is a non-model task executed by the dedicated
-- RenderWorker; the generic agent worker claims the five product teams only, so
-- these tasks must live outside those teams or the agent worker claims and kills
-- them with invalid_work_type (acceptance S1, P7-BUG-01).
--
-- Current constraint name is chk_work_item_team (agent_0004_code_team.sql #31);
-- the original inline CHECK was auto-named work_item_team_check. DROP IF EXISTS
-- on both names keeps this migration safe on fresh and pre-P5 databases.
-- Additive CHECK widening only; no column/data change and no team room for ops.

ALTER TABLE work_item DROP CONSTRAINT IF EXISTS chk_work_item_team;
ALTER TABLE work_item DROP CONSTRAINT IF EXISTS work_item_team_check;
ALTER TABLE work_item ADD CONSTRAINT chk_work_item_team
    CHECK (team IN ('clarification', 'design', 'execution', 'delivery', 'steward', 'code', 'ops'));
