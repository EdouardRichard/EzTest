-- P4 delivery context: analysis run (#23).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p4-delivery-chain-design.md §3.4.
-- version/agent_run references are logical BIGINT only; IDs are
-- application-generated snowflakes (D29). P4 only writes kind 'triage'
-- (issue.triage); failure/coverage stay reserved enum values (P4-D-05).

CREATE TABLE delivery_analysis_run (
    id              BIGINT NOT NULL,
    version_id      BIGINT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('failure', 'coverage', 'triage')),
    status          TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'running', 'done', 'failed', 'degraded')),
    dedupe_key      TEXT NOT NULL CHECK (char_length(dedupe_key) BETWEEN 1 AND 200),
    input_ref       JSONB NOT NULL DEFAULT '{}'::jsonb,
    output          JSONB NOT NULL DEFAULT '{}'::jsonb,
    agent_run_id    BIGINT NULL,
    error           TEXT NOT NULL DEFAULT '' CHECK (char_length(error) <= 2000),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_delivery_analysis_run PRIMARY KEY (id)
);

-- At most one active run per idempotency key; finished runs keep history.
CREATE UNIQUE INDEX uk_delivery_analysis_run_version_id_kind_dedupe_key
    ON delivery_analysis_run (version_id, kind, dedupe_key)
    WHERE status IN ('queued', 'running');
CREATE UNIQUE INDEX uk_delivery_analysis_run_agent_run_id
    ON delivery_analysis_run (agent_run_id)
    WHERE agent_run_id IS NOT NULL;
CREATE INDEX idx_delivery_analysis_run_version_id_kind_id
    ON delivery_analysis_run (version_id, kind, id DESC);
