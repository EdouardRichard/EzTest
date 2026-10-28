-- P5 (D37): code anchors may point at a mindmap case node. Widen the existing
-- CHECK constraint; the legacy constraint keeps its plural name because D34
-- renames tables only. The existing version/requirement/app_model behaviour is
-- unchanged.
ALTER TABLE code_anchor DROP CONSTRAINT IF EXISTS code_anchors_subject_kind_check;
ALTER TABLE code_anchor ADD CONSTRAINT ck_code_anchor_subject_kind
  CHECK (subject_kind IN ('version','requirement','app_model','case'));
