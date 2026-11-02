-- P3 execution context: executor instance registry and human requests.
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md §3.3.
-- executor_instance is reserved for P7 multi-executor scheduling; P3 registers a
-- single endpoint through API pull healthz (P3-D-01). human_request belongs to the
-- execution migration even though the domain object is version-scoped (P3-D-16).

CREATE TABLE executor_instance (
    id                  BIGINT NOT NULL,
    name                TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    endpoint            TEXT NOT NULL CHECK (char_length(endpoint) BETWEEN 1 AND 500),
    token_hash          TEXT NOT NULL DEFAULT ''
                        CHECK (token_hash = '' OR token_hash ~ '^[0-9a-f]{64}$'),
    capacity            INTEGER NOT NULL DEFAULT 0 CHECK (capacity >= 0),
    active_contexts     INTEGER NOT NULL DEFAULT 0 CHECK (active_contexts >= 0),
    labels              JSONB NOT NULL DEFAULT '{}'::jsonb
                        CHECK (jsonb_typeof(labels) = 'object'),
    status              TEXT NOT NULL DEFAULT 'offline'
                        CHECK (status IN ('active', 'draining', 'offline')),
    last_heartbeat      TIMESTAMPTZ NULL,
    version             TEXT NOT NULL DEFAULT '' CHECK (char_length(version) <= 64),
    last_error          TEXT NOT NULL DEFAULT '' CHECK (char_length(last_error) <= 2000),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_executor_instance PRIMARY KEY (id),
    CONSTRAINT uk_executor_instance_endpoint UNIQUE (endpoint)
);

CREATE INDEX idx_executor_instance_status ON executor_instance (status, last_heartbeat);

CREATE TABLE human_request (
    id              BIGINT NOT NULL,
    version_id      BIGINT NOT NULL,
    case_run_id     BIGINT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('login_takeover', 'conflict', 'other')),
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb
                    CHECK (jsonb_typeof(payload) = 'object'),
    status          TEXT NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'resolved', 'cancelled', 'expired')),
    response        JSONB NOT NULL DEFAULT '{}'::jsonb
                    CHECK (jsonb_typeof(response) = 'object'),
    expires_at      TIMESTAMPTZ NOT NULL,
    resolved_by     BIGINT NULL,
    resolved_at     TIMESTAMPTZ NULL,
    created_by      BIGINT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_human_request PRIMARY KEY (id)
);

CREATE INDEX idx_human_request_version_status
    ON human_request (version_id, status, id DESC);
CREATE INDEX idx_human_request_case_run ON human_request (case_run_id, status);
CREATE INDEX idx_human_request_expiry ON human_request (status, expires_at);
