-- v0.2.3 legacy profile backfill marker and retired profile rows (spec §3.3,
-- §5; research global §G-01 and clarify §C-02).
--
-- Existing agent_profile rows were inserted by the startup seed with
-- ON CONFLICT DO NOTHING, so a 0.2.1 database keeps its old allowed_tools,
-- budgets and prompt forever. seed_version starts at 0 for every legacy row; the
-- startup backfill reconciles a row once and writes the current seed version.
-- Fresh rows are inserted by EnsureProfile at the current version.
ALTER TABLE agent_profile
    ADD COLUMN seed_version INTEGER NOT NULL DEFAULT 0 CHECK (seed_version >= 0);

-- The five profiles removed by v0.22 D-B1/R-B1-2 can remain in an upgraded
-- database. Keep the rows and their history for audit, but never run or edit
-- them again; the admin list documents 16 -> 11 built-in profiles.
UPDATE agent_profile
   SET enabled = FALSE
 WHERE key IN (
    'requirement_curator',
    'context_scout',
    'design_planner',
    'case_author',
    'data_planner'
 );
