-- v0.2.4-B (T8): the requirement natural-language contract (design v1.3.1
-- §5.1 "V1 澄清参数" + §3.6 "ContentHash 重组").
--
-- requirement_version stops describing a requirement by resource IDs and starts
-- describing it in the ISO/IEC/IEEE 29148 shapes the clarification gate and the
-- design/judge subprojects consume:
--
--   user_story          {as_a, i_want, so_that}      NEW  (§5.1)
--   rules               free text, optional          NEW  (§5.1)
--   acceptance_criteria TEXT newline list -> JSONB array of
--                         {id, given, when, then}    RESHAPED (§5.1/§3.6)
--   sut_description     the system under test, in
--                       natural language             NEW  (§5.1, GD-22)
--   scope_out           optional                     NEW  (§5.1)
--   account_roles_hint  optional string array        NEW  (§5.1)
--
-- Dropped, because the "target" concept is inverted away (a version binds an
-- environment and the requirement says what it means in prose; nothing selects
-- resource IDs any more):
--
--   requirement_version.description          replaced by user_story + rules (§3.6)
--   requirement_version.target_version_suts
--   requirement_version.target_pages
--   requirement_version.target_apis
--   requirement.target_version_sut_ids
--
-- Every new column is NOT NULL with a default, matching how version_0002 defined
-- title/description/acceptance_criteria/constraints/the target arrays. That is
-- load-bearing, not cosmetic: the Go readers scan rules/sut_description/scope_out
-- into plain strings and the two JSONB documents into []byte, so a NULL would be
-- a scan failure at runtime rather than an empty value. Optionality in the §5.1
-- contract is expressed as "may be empty", which is what a NOT NULL DEFAULT ''
-- column holds.
--
-- acceptance_criteria.id is SERVER-ASSIGNED ("AC-<n>") and stable across
-- revisions by identical content; the numbering authority lives in
-- version/domain/acceptance_criteria.go (AssignACIDs), not here, so the column
-- only has to hold the resulting document. A GIN index is deliberately NOT
-- added: the AC set is read with its own requirement_version row, never queried
-- by AC id.
--
-- GD-05 (destructive, owner-approved): ContentHash necessarily changes for every
-- existing revision, because both the payload shape (design §3.6) and the ID
-- domain changed. There is no backfill and no dual-track compatibility layer;
-- version_0016_v024_environment_not_null.sql already TRUNCATEd the version
-- closure, so requirement / requirement_version are empty when this runs.
-- Append-only: this file supersedes version_0002_requirement.sql instead of
-- editing it.

ALTER TABLE requirement_version
    ADD COLUMN user_story         JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN rules              TEXT  NOT NULL DEFAULT '',
    ADD COLUMN sut_description    TEXT  NOT NULL DEFAULT '',
    ADD COLUMN scope_out          TEXT  NOT NULL DEFAULT '',
    ADD COLUMN account_roles_hint JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE requirement_version
    ADD CONSTRAINT requirement_version_user_story_check
        CHECK (jsonb_typeof(user_story) = 'object'),
    ADD CONSTRAINT requirement_version_account_roles_hint_check
        CHECK (jsonb_typeof(account_roles_hint) = 'array');

-- TEXT -> JSONB. The old value was a newline-separated list with no ids, so it
-- cannot be converted into the {id, given, when, then} document; GD-05 discards
-- it and every row starts from the empty array. DROP DEFAULT first: Postgres
-- would otherwise try to coerce the old ''::text default to jsonb and fail.
ALTER TABLE requirement_version ALTER COLUMN acceptance_criteria DROP DEFAULT;
ALTER TABLE requirement_version
    ALTER COLUMN acceptance_criteria TYPE JSONB USING '[]'::jsonb;
ALTER TABLE requirement_version ALTER COLUMN acceptance_criteria SET DEFAULT '[]'::jsonb;
ALTER TABLE requirement_version
    ADD CONSTRAINT requirement_version_acceptance_criteria_check
        CHECK (jsonb_typeof(acceptance_criteria) = 'array');

ALTER TABLE requirement_version
    DROP COLUMN description,
    DROP COLUMN target_version_suts,
    DROP COLUMN target_pages,
    DROP COLUMN target_apis;

ALTER TABLE requirement DROP COLUMN target_version_sut_ids;
