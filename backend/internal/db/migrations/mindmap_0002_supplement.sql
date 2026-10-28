-- P7 mindmap context (spec §4.2.7): supplement and incremental case-design
-- runs carry the batch they belong to. Replacing the auto-named CHECK keeps
-- every legacy kind and adds the two P7 kinds.
ALTER TABLE case_design_run DROP CONSTRAINT IF EXISTS case_design_run_kind_check;
ALTER TABLE case_design_run ADD CONSTRAINT ck_case_design_run_kind CHECK (kind IN
  ('explore','design','review','data_plan','supplement','incremental_design'));
ALTER TABLE case_design_run ADD COLUMN IF NOT EXISTS batch_id BIGINT;
CREATE UNIQUE INDEX IF NOT EXISTS uk_case_design_run_supplement_batch
  ON case_design_run(batch_id, kind)
  WHERE kind IN ('supplement','incremental_design') AND status IN ('queued','running');
CREATE INDEX IF NOT EXISTS idx_case_design_run_batch
  ON case_design_run(batch_id, kind, created_at DESC);
