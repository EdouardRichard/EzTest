-- P8 visual evidence (design spec §9.3 / D7): the execution context records the
-- actual screenshot and the pixel-diff image of a visual assertion, and the
-- evidence GC already protects visual_baseline artifacts.
--
-- assertion_result.kind is an inline CHECK named assertion_result_kind_check;
-- evidence.kind is the inline evidence_kind_check. Both were re-created under
-- explicit names by the P7 migration (ck_*), so every historical name is
-- dropped before the widened constraint is added.
ALTER TABLE assertion_result DROP CONSTRAINT IF EXISTS assertion_result_kind_check;
ALTER TABLE assertion_result DROP CONSTRAINT IF EXISTS ck_assertion_result_kind;
ALTER TABLE assertion_result ADD CONSTRAINT ck_assertion_result_kind
  CHECK (kind IN ('ui','api','db','text','visual'));

ALTER TABLE evidence DROP CONSTRAINT IF EXISTS evidence_kind_check;
ALTER TABLE evidence DROP CONSTRAINT IF EXISTS ck_evidence_kind;
ALTER TABLE evidence ADD CONSTRAINT ck_evidence_kind CHECK (kind IN
  ('screenshot','http_exchange','console_log','network_log','download','db_result',
   'visual_baseline','visual_actual','visual_diff'));
