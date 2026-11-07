-- v0.22 EX execution-time exploration schema: the frozen wave scope snapshot
-- (spec DB-19, R-EX-4) and normalized execution-time discovery observations
-- (spec DB-29, R-EX-1..R-EX-3).
--
-- Observations keep only normalized resource templates (page = route/path,
-- api = path) plus a fingerprint; query values, headers, cookies, bodies and
-- raw request/response payloads are deliberately never stored (N-16, R-EX-7).
-- IDs are logical BIGINT references into the version/execution contexts
-- (Agent.md §6.9.7), so no cross-context foreign keys. version_sut_id is part
-- of the observation identity and therefore NOT NULL: with NULLS DISTINCT a
-- nullable identity column would defeat dedupe (Lead ruling).
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-19/DB-29
-- and §4.20/§4.31.

ALTER TABLE run_wave
    ADD COLUMN scope_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT chk_run_wave_scope_snapshot
        CHECK (jsonb_typeof(scope_snapshot) = 'object');

CREATE TABLE execution_discovery_observation (
    id                  BIGINT NOT NULL,
    version_id          BIGINT NOT NULL,
    wave_id             BIGINT NULL,
    case_run_id         BIGINT NULL,
    version_sut_id      BIGINT NOT NULL,
    environment_id      BIGINT NULL,
    kind                TEXT NOT NULL
                        CONSTRAINT chk_execution_discovery_observation_kind
                        CHECK (kind IN ('page', 'api')),
    method              TEXT NOT NULL DEFAULT '',
    host                TEXT NOT NULL DEFAULT '',
    resource_template   TEXT NOT NULL,
    status_code         INTEGER NULL,
    mime                TEXT NOT NULL DEFAULT '',
    source_kind         TEXT NOT NULL DEFAULT 'execution'
                        CONSTRAINT chk_execution_discovery_observation_source_kind
                        CHECK (source_kind IN ('execution', 'p9')),
    fingerprint         TEXT NOT NULL DEFAULT '',
    occurrences         INTEGER NOT NULL DEFAULT 1
                        CONSTRAINT chk_execution_discovery_observation_occurrences
                        CHECK (occurrences > 0),
    first_seen_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    decision            TEXT NOT NULL DEFAULT 'new'
                        CONSTRAINT chk_execution_discovery_observation_decision
                        CHECK (decision IN ('new', 'proposed', 'approved', 'rejected', 'out_of_scope')),
    resource_id         BIGINT NULL,
    fact_id             BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_execution_discovery_observation PRIMARY KEY (id),
    CONSTRAINT uk_execution_discovery_observation
        UNIQUE (version_id, version_sut_id, kind, method, resource_template)
);

CREATE INDEX idx_execution_discovery_observation_version_decision
    ON execution_discovery_observation (version_id, decision);
CREATE INDEX idx_execution_discovery_observation_wave
    ON execution_discovery_observation (wave_id);
CREATE INDEX idx_execution_discovery_observation_resource
    ON execution_discovery_observation (resource_id);
