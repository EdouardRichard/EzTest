-- P7 codeintel context (spec §4.2.8): issue code anchors reuse the P5
-- subject_kind widening. Replace the named CHECK introduced by codeintel_0004
-- so every existing subject kind keeps working and 'issue' is added.
ALTER TABLE code_anchor DROP CONSTRAINT IF EXISTS ck_code_anchor_subject_kind;
ALTER TABLE code_anchor ADD CONSTRAINT ck_code_anchor_subject_kind CHECK (subject_kind IN
  ('version','requirement','app_model','case','issue'));
