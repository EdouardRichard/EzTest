-- P7 orchestration context: API instance registry (#37).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md §5 (#37) and
-- §4.5. Each API replica registers itself, heartbeats and may be drained. node_id
-- is unique per live instance (reclaimed only after offline/stopped). IDs are
-- application-generated snowflakes; the table is a single coordination surface
-- (no cross-context FK).

CREATE TABLE api_instance (
    id           BIGINT NOT NULL,
    instance_key TEXT NOT NULL,
    hostname     TEXT NOT NULL,
    pid          INTEGER NOT NULL CHECK (pid > 0),
    api_port     INTEGER NOT NULL CHECK (api_port BETWEEN 1 AND 65535),
    node_id      INTEGER NOT NULL CHECK (node_id BETWEEN 0 AND 1023),
    status       TEXT NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active', 'draining', 'offline', 'stopped')),
    version      TEXT NOT NULL DEFAULT '',
    last_error   TEXT NULL,
    started_at   TIMESTAMPTZ NOT NULL,
    heartbeat_at TIMESTAMPTZ NOT NULL,
    draining_at  TIMESTAMPTZ NULL,
    stopped_at   TIMESTAMPTZ NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_api_instance PRIMARY KEY (id),
    CONSTRAINT uk_api_instance_instance_key UNIQUE (instance_key),
    CONSTRAINT uk_api_instance_node_id UNIQUE (node_id)
);

CREATE INDEX idx_api_instance_status_heartbeat ON api_instance (status, heartbeat_at DESC);
