-- P6 quality context: real-LLM evaluation runs and per-metric results (#32).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p6-parity-quality-search-agent-center-design.md §3.2.
-- Baseline lives in eval_metric.detail.baseline; metric names are validated by
-- the service, not the DB (spec §5.3).

CREATE TABLE eval_run (
    id             BIGINT NOT NULL,
    mode           TEXT NOT NULL CHECK (mode = 'real_llm'),
    corpus_version TEXT NOT NULL CHECK (corpus_version <> ''),
    model          TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL CHECK (status IN ('running','passed','failed','error')),
    summary        JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object'),
    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_eval_run PRIMARY KEY (id)
);
CREATE INDEX idx_eval_run_created_at ON eval_run (created_at DESC, id DESC);

CREATE TABLE eval_metric (
    run_id     BIGINT NOT NULL,
    name       TEXT NOT NULL CHECK (name <> ''),
    value      NUMERIC(8,5) NOT NULL,
    threshold  NUMERIC(8,5) NOT NULL,
    passed     BOOLEAN NOT NULL,
    detail     JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(detail) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_eval_metric PRIMARY KEY (run_id, name),
    CONSTRAINT fk_eval_metric_run FOREIGN KEY (run_id) REFERENCES eval_run(id) ON DELETE CASCADE
);
CREATE INDEX idx_eval_metric_run ON eval_metric (run_id, name);
