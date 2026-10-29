-- P8 quality context (design spec §4.1/§4.2/§9.1, D61): the evaluation runs and
-- their five KPI groups. A run stores only ids/counters/verdicts; no corpus
-- content, prompt, response or credential is persisted. The deterministic track
-- is the CI gate; the real_llm track is a report (the runner decides the exit
-- code, the API only records the verdict).
CREATE TABLE IF NOT EXISTS eval_run (
  id             BIGINT NOT NULL,
  mode           TEXT NOT NULL CHECK (mode IN ('deterministic','real_llm')),
  corpus_version TEXT NOT NULL,
  model          TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL CHECK (status IN ('running','passed','failed','error')),
  summary        JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at    TIMESTAMPTZ,
  CONSTRAINT pk_eval_run PRIMARY KEY (id)
);
CREATE INDEX IF NOT EXISTS idx_eval_run_history
  ON eval_run(started_at DESC, id DESC);

-- One KPI per (run, name). The composite primary key is the natural idempotency
-- key: a retried POST can never duplicate a metric of the same run.
CREATE TABLE IF NOT EXISTS eval_metric (
  run_id    BIGINT NOT NULL,
  name      TEXT NOT NULL,
  value     NUMERIC(8,5) NOT NULL,
  threshold NUMERIC(8,5) NOT NULL,
  passed    BOOLEAN NOT NULL,
  detail    JSONB NOT NULL DEFAULT '{}'::jsonb,
  CONSTRAINT pk_eval_metric PRIMARY KEY (run_id, name)
);
CREATE INDEX IF NOT EXISTS idx_eval_metric_run
  ON eval_metric(run_id, name);
