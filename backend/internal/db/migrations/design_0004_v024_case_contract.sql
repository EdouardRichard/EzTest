-- v0.2.4-B (T9): the case natural-language contract (design v1.3.1 §3.2
-- "scenario / case_version / case_step / case_assertion" + §3.6 "ContentHash
-- 重组" + §5.2 "用例契约").
--
-- A case version stops describing what it tests by resource IDs and starts
-- describing it in the Gherkin-isomorphic prose the design author, the design
-- judge and the execution team consume:
--
--   case_version.sut_description   the system under test, in natural
--                                  language                    NEW  (§5.2)
--   case_version.covered_acs       JSONB array of the requirement's
--                                  acceptance-criterion ids it
--                                  implements ("AC-<n>")       NEW  (§5.2/§3.6)
--   case_version.preconditions     already present; now a REQUIRED contract
--                                  element (↔ Given, explicit "无" allowed)
--                                                                (§5.2)
--   case_step.target_description   free text                     NEW  (§3.2)
--   case_assertion.sut_id          renamed from database_id: a db assertion
--                                  names the SUT, not a catalog database row
--                                                                (§3.2, M-B4)
--
-- Dropped, because the "target" concept is inverted away (a version binds an
-- environment, the requirement says what it means in prose, and the case says
-- which acceptance criteria it covers; nothing selects resource IDs any more):
--
--   case_scenario.version_sut_ids / page_ids / api_ids           (§3.2)
--   case_version.default_version_sut_id / version_sut_ids /
--                page_ids / api_ids                              (§3.2)
--   case_step.target_version_sut_id / target_page_id /
--           target_api_id                                        (§3.2)
--   case_assertion.target_version_sut_id / target_api_id         (§3.2)
--
-- case_assertion.kind already lost 'visual' in visual_0002_v024_drop_visual.sql
-- (GD-22), so the value set here is exactly ui/api/db/text and needs no change.
--
-- Every new column is NOT NULL with a default (C10 lesson, restated by T8's
-- version_0017): the Go readers scan sut_description/target_description into
-- plain strings and covered_acs into []byte, so a NULL would be a runtime scan
-- failure ("cannot scan NULL into *string") rather than an empty value.
-- Optionality in the §5.2 contract is expressed as "may be empty", which is
-- what a NOT NULL DEFAULT '' / '[]' column holds.
--
-- The REQUIRED-ness of sut_description (1..2000), covered_acs (>= 1 and a
-- subset of the requirement's AC set) and preconditions (>= 1, explicit "无"
-- allowed) is enforced in the domain gate (design.ValidateProposal), not by a
-- column CHECK -- the same split T8 used for requirement_version, where §5.1
-- requires sut_description 1..2000 while the column stays DEFAULT ''. A CHECK
-- here would reject the lenient draft/fixture writes that never go through the
-- proposal gate. The upper bounds ARE column-checked, because no caller may
-- ever store more text than the contract allows.
--
-- GD-05 (destructive, owner-approved): ContentHash necessarily changes for
-- every existing case version, because both the payload shape (§3.6) and the ID
-- domain changed. There is no backfill and no dual-track compatibility layer;
-- version_0016_v024_environment_not_null.sql already TRUNCATEd the version
-- closure, so case_scenario / case_entity / case_version are empty when this
-- runs. Append-only: this file supersedes design_0001_case_model.sql instead of
-- editing it.

ALTER TABLE case_version
    ADD COLUMN sut_description TEXT  NOT NULL DEFAULT '',
    ADD COLUMN covered_acs     JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE case_version
    ADD CONSTRAINT case_version_sut_description_check
        CHECK (char_length(sut_description) <= 2000),
    ADD CONSTRAINT case_version_covered_acs_check
        CHECK (jsonb_typeof(covered_acs) = 'array');

ALTER TABLE case_version
    DROP COLUMN default_version_sut_id,
    DROP COLUMN version_sut_ids,
    DROP COLUMN page_ids,
    DROP COLUMN api_ids;

ALTER TABLE case_step
    ADD COLUMN target_description TEXT NOT NULL DEFAULT '';

ALTER TABLE case_step
    ADD CONSTRAINT case_step_target_description_check
        CHECK (char_length(target_description) <= 2000);

ALTER TABLE case_step
    DROP COLUMN target_version_sut_id,
    DROP COLUMN target_page_id,
    DROP COLUMN target_api_id;

-- database_id -> sut_id (M-B4): the column keeps its BIGINT NULL shape and its
-- meaning narrows to "the SUT a db assertion reads", so a RENAME carries the
-- data instead of a drop/add pair. No cross-context foreign key (Agent.md
-- §6.9.7); design only checks that a db assertion names one.
ALTER TABLE case_assertion RENAME COLUMN database_id TO sut_id;

ALTER TABLE case_assertion
    DROP COLUMN target_version_sut_id,
    DROP COLUMN target_api_id;

ALTER TABLE case_scenario
    DROP COLUMN version_sut_ids,
    DROP COLUMN page_ids,
    DROP COLUMN api_ids;
