-- v0.2.4-E (T2): independent design judge facts (design v1.3.1 §5.3, spec SC-E5).
-- Append-only judgment per case version; override is a separate audit row.
CREATE TABLE design_judgment (
    id                 BIGINT PRIMARY KEY,
    version_id         BIGINT NOT NULL,
    requirement_id     BIGINT NOT NULL,
    case_id            BIGINT NOT NULL,
    case_version_id    BIGINT NOT NULL,
    content_hash       TEXT   NOT NULL,
    round              INTEGER NOT NULL,
    verdict            TEXT   NOT NULL CHECK (verdict IN ('pass','fail')),
    findings           JSONB  NOT NULL DEFAULT '[]',
    ac_coverage_matrix JSONB  NOT NULL DEFAULT '{}',
    agent_run_id       BIGINT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (case_id, case_version_id)
);
CREATE INDEX idx_design_judgment_version ON design_judgment (version_id, requirement_id);

CREATE TABLE design_judgment_override (
    id              BIGINT PRIMARY KEY,
    case_id         BIGINT NOT NULL,
    case_version_id BIGINT NOT NULL,
    content_hash    TEXT   NOT NULL,
    judgment_id     BIGINT,
    condition       TEXT   NOT NULL CHECK (condition IN ('fail_exhausted','judge_exhausted')),
    actor_id        BIGINT NOT NULL,
    reason          TEXT   NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_design_judgment_override_case ON design_judgment_override (case_id, case_version_id);
