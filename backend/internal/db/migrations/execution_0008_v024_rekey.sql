-- v0.2.4-B (T11): mechanical rekey of the execution context from the dropped
-- version_sut binding-row id to the SUT id.
--
-- W2 (version_0015_v024_environment_binding) dropped the version_sut chain: a
-- version now binds an environment directly, so every execution column that
-- used to hold a binding-row id already holds a SUT id (the Go adapters have
-- been filling SUT ids since W2). This migration only renames the columns --
-- plus the one index and the one auto-named CHECK whose names still say
-- version_sut -- so the persisted schema matches the id domain it carries. It
-- is a MECHANICAL rekey: no row changes meaning and no predicate is added,
-- removed or widened.
--
-- The two UNIQUE constraints keep their names (uk_case_run_sut_context,
-- uk_execution_discovery_observation): RENAME COLUMN rewrites their definitions
-- in place, so the (case_run, SUT, account_role) executor-context identity and
-- the (version, SUT, kind, method, resource_template) observation identity are
-- unchanged apart from the id domain. Both stay NOT NULL -- the observation key
-- is an identity column and must not become nullable (execution_0006 ruling).
--
-- Three JSONB documents carry the old names as DATA rather than DDL, so they are
-- rewritten with jsonb operators instead of ALTER TABLE:
--   run_wave.counts.by_version_sut                -> by_sut
--   run_wave.scope_snapshot.version_suts          -> suts
--   run_wave.scope_snapshot.suts[].version_sut_id -> dropped (the entry already
--                                                    carries the same value as
--                                                    sut_id since W2)
--   human_request.payload.version_sut_id          -> sut_id
-- GD-05: v0.2.4 rebuilds from a fresh database, so these UPDATEs are a no-op
-- there; they are kept so the migration is also correct on a copied dataset.
--
-- NOT changed here (subproject F owns the semantics): the environment
-- fingerprint's freeze/refresh rules, what counts as an active binding, and the
-- commit-vs-branch baseline precedence. The fingerprint HASH VALUES do change,
-- because the hashed payload loses version_sut_id and its id domain is now the
-- SUT id; GD-05 discards the pre-upgrade intents, which is also why no
-- uk_execution_intent_active repair is needed.
--
-- Companion release-note item: the executor context key moves from
-- run:<id>:vs:<sut>:role:<role> to run:<id>:sut:<sut>:role:<role> in the same
-- commit, so executor contexts created before the upgrade no longer match orphan
-- reconciliation (accepted under GD-05).

-- 1. execution_intent: the intent's target SUT id set (JSONB array). The inline
--    column CHECK was auto-named by PostgreSQL, so it is renamed explicitly.
ALTER TABLE execution_intent RENAME COLUMN version_sut_ids TO sut_ids;
ALTER TABLE execution_intent
    RENAME CONSTRAINT execution_intent_version_sut_ids_check
    TO execution_intent_sut_ids_check;

-- 2. Step and assertion targets (both nullable: an empty target inherits the
--    case version's default SUT).
ALTER TABLE step_result RENAME COLUMN target_version_sut_id TO target_sut_id;
ALTER TABLE assertion_result RENAME COLUMN target_version_sut_id TO target_sut_id;

-- 3. Evidence ownership plus its descending-id lookup index.
ALTER TABLE evidence RENAME COLUMN version_sut_id TO sut_id;
ALTER INDEX idx_evidence_version_sut RENAME TO idx_evidence_sut;

-- 4. Per-run executor context identity.
ALTER TABLE case_run_sut_context RENAME COLUMN version_sut_id TO sut_id;

-- 5. Execution-time discovery observation identity.
ALTER TABLE execution_discovery_observation RENAME COLUMN version_sut_id TO sut_id;

-- 6. JSONB data keys. Each UPDATE is guarded by the key-existence operator so a
--    row that never carried the old key is left byte-identical.

-- 6a. run_wave.counts: by_version_sut -> by_sut, value preserved as-is.
--     (jsonb normalizes key order on storage, so `- key || build_object` does
--     not disturb any ordering contract; readers unmarshal by name.)
UPDATE run_wave
SET counts = (counts - 'by_version_sut')
             || jsonb_build_object('by_sut', counts -> 'by_version_sut')
WHERE counts ? 'by_version_sut';

-- 6b. run_wave.scope_snapshot: version_suts -> suts, dropping each entry's
--     redundant version_sut_id (since W2 both legs carried the same SUT id).
--     A snapshot whose version_suts is absent or is not an array is left
--     byte-identical rather than failing the migration: jsonb_array_elements
--     would error on a scalar, and the typeof guard is what makes this safe on
--     a hand-edited or partially written row.
UPDATE run_wave
SET scope_snapshot = (scope_snapshot - 'version_suts')
                     || jsonb_build_object('suts', COALESCE((
                            SELECT jsonb_agg(entry - 'version_sut_id')
                            FROM jsonb_array_elements(scope_snapshot -> 'version_suts') AS entry
                        ), '[]'::jsonb))
WHERE jsonb_typeof(scope_snapshot -> 'version_suts') = 'array';

-- 6c. human_request.payload: version_sut_id -> sut_id.
UPDATE human_request
SET payload = (payload - 'version_sut_id')
              || jsonb_build_object('sut_id', payload -> 'version_sut_id')
WHERE payload ? 'version_sut_id';
