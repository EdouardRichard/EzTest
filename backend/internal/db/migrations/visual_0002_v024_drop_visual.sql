-- GD-22: remove the visual assertion domain (pixel regression deferred to a
-- later release). Drops the three visual tables in FK order and restores the
-- kind CHECK constraints to their pre-visual value sets:
--   evidence.kind         -> execution_0002 value set, keeping the
--                            chk_evidence_kind name introduced by execution_0004
--   case_assertion.kind   -> design_0001 value set minus 'visual'
--   assertion_result.kind -> execution_0002 value set minus 'visual'
-- Destructive by ruling (GD-05): visual data is discardable. Append-only: this
-- file supersedes visual_0001/execution_0004 instead of editing them.

DROP TABLE IF EXISTS visual_diff;
DROP TABLE IF EXISTS visual_baseline;
DROP TABLE IF EXISTS visual_assertion;

ALTER TABLE evidence DROP CONSTRAINT IF EXISTS chk_evidence_kind;
ALTER TABLE evidence ADD CONSTRAINT chk_evidence_kind
    CHECK (kind IN ('screenshot', 'snapshot', 'console', 'network',
                    'http', 'db', 'download', 'text'));

ALTER TABLE case_assertion DROP CONSTRAINT IF EXISTS case_assertion_kind_check;
ALTER TABLE case_assertion ADD CONSTRAINT case_assertion_kind_check
    CHECK (kind IN ('ui', 'api', 'db', 'text'));

ALTER TABLE assertion_result DROP CONSTRAINT IF EXISTS assertion_result_kind_check;
ALTER TABLE assertion_result ADD CONSTRAINT assertion_result_kind_check
    CHECK (kind IN ('ui', 'api', 'db', 'text'));
