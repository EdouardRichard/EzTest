-- v0.2.4-B (GD-05, owner-approved destructive): make the direct environment
-- binding mandatory.
--
-- version_0015_v024_environment_binding.sql added version.environment_id as a
-- NULLABLE logical BIGINT, because an ALTER cannot introduce a mandatory column
-- on a populated table. A version without an environment violates the domain
-- invariant (version.Version.Validate rejects EnvironmentID <= 0 and the create
-- use case returns a field-level 400), so the database must not be able to hold
-- that state either. GD-05 makes pre-v0.2.4-B version rows discardable: they were
-- bound through the version_sut chain that version_0015 dropped, so none of them
-- has an environment left to keep.
--
-- Why TRUNCATE ... CASCADE and not DELETE FROM version:
-- three of the five foreign keys into version are declared NO ACTION, not
-- CASCADE, so a bare DELETE raises a foreign_key_violation on any populated
-- database. Verified against the live schema (pg_constraint, confdeltype):
--
--   version <- version_fact                CASCADE
--   version <- version_plan_item           CASCADE
--   version <- conversation                NO ACTION   <- blocks DELETE
--   version <- requirement                 NO ACTION   <- blocks DELETE
--   version <- requirement_extraction_run  NO ACTION   <- blocks DELETE
--
-- TRUNCATE ... CASCADE follows the schema's own declared dependency graph
-- instead of a hand-written delete order, so it cannot drift from it. It clears
-- exactly these 13 tables (the full transitive closure of version):
--
--   version, version_fact, version_plan_item,
--   conversation, conversation_message, conversation_message_part,
--   conversation_summary,
--   requirement, requirement_version, requirement_confirmation,
--   requirement_closure,
--   requirement_extraction_run, requirement_extraction_segment
--
-- version_account carries a LOGICAL version_id (no foreign key, Agent.md
-- §6.9.7) and is owned by this context, so it is cleared explicitly rather than
-- left holding rows that can never resolve to a version.
--
-- DELIBERATELY NOT cleared here -- these tables also carry a logical version_id
-- with no foreign key, so they are left orphaned. They belong to other bounded
-- contexts and are rekeyed by later v0.2.4-B tasks; wiping six contexts' data
-- from a version-context migration is out of scope. GD-05 discards this data and
-- a freshly migrated database is the supported path:
--
--   case_scenario, clarification_handoff, code_analysis, code_anchor,
--   code_index_run, context_packet, delivery_analysis_run, delivery_issue,
--   delivery_report, design_round, design_scope, eval_run,
--   execution_discovery_observation, execution_intent, export_artifact,
--   human_request, requirement_attachment (and its own dependents
--   attachment_image / content_object / content_ref), requirement_relation,
--   run_wave, team_room_digest, team_room_post, work_item

DELETE FROM version_account;

TRUNCATE version CASCADE;

ALTER TABLE version ALTER COLUMN environment_id SET NOT NULL;

-- The partial predicate added by version_0015 is now always true, so the index
-- becomes a plain one; keeping the predicate would be a second, stale statement
-- of the column's nullability.
DROP INDEX IF EXISTS idx_version_environment;
CREATE INDEX idx_version_environment ON version (environment_id);
