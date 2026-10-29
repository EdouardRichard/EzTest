-- P7 execution context (spec §4.2.6): batch kinds and parent links, the
-- current working-graph hash, the autonomy flag, the case-run source and the
-- db_result evidence kind.
--
-- Order matters: add graph_hash nullable, backfill it from baseline_hash, make
-- it NOT NULL, then add its CHECK constraint. autonomy_done is backfilled true
-- for P6 terminal batches so the autonomous worker never re-runs history.
ALTER TABLE run_batch ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'baseline'
  CHECK (kind IN ('baseline','incremental','supplement'));
ALTER TABLE run_batch ADD COLUMN IF NOT EXISTS parent_batch_id BIGINT;
ALTER TABLE run_batch ADD COLUMN IF NOT EXISTS base_fallback_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE run_batch ADD COLUMN IF NOT EXISTS graph_hash TEXT;
UPDATE run_batch SET graph_hash = baseline_hash WHERE graph_hash IS NULL;
ALTER TABLE run_batch ALTER COLUMN graph_hash SET NOT NULL;
ALTER TABLE run_batch ADD COLUMN IF NOT EXISTS supplement_attempted BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE run_batch ADD COLUMN IF NOT EXISTS autonomy_done BOOLEAN NOT NULL DEFAULT false;
UPDATE run_batch SET autonomy_done = true WHERE status IN ('done','failed','cancelled');
ALTER TABLE run_batch ADD CONSTRAINT ck_run_batch_graph_hash
  CHECK (graph_hash ~ '^[0-9a-f]{64}$');
ALTER TABLE run_batch ADD CONSTRAINT ck_run_batch_supplement_parent
  CHECK (kind <> 'supplement' OR parent_batch_id IS NOT NULL);
CREATE INDEX IF NOT EXISTS idx_run_batch_parent ON run_batch(parent_batch_id, id);

ALTER TABLE case_run ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'baseline'
  CHECK (source IN ('baseline','ai_supplement'));

ALTER TABLE evidence DROP CONSTRAINT IF EXISTS evidence_kind_check;
ALTER TABLE evidence ADD CONSTRAINT ck_evidence_kind CHECK (kind IN
  ('screenshot','http_exchange','console_log','network_log','download','db_result'));
