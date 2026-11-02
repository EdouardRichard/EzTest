-- P1 agent context: the single execution queue and its attempt log.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md §3.1.

CREATE TABLE work_item (
    id            BIGINT PRIMARY KEY,
    version_id    BIGINT NOT NULL,
    team          TEXT NOT NULL CHECK (team IN ('clarification', 'design', 'execution', 'delivery', 'steward')),
    type          TEXT NOT NULL CHECK (type <> ''),
    status        TEXT NOT NULL DEFAULT 'open'
                  CHECK (status IN ('open', 'claimed', 'running', 'waiting_human', 'done', 'failed', 'dead')),
    priority      INTEGER NOT NULL DEFAULT 0,
    dedupe_key    TEXT NOT NULL CHECK (dedupe_key <> ''),
    depends_on    BIGINT NULL,
    payload       JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    claimed_by    TEXT NULL,
    lease_until   TIMESTAMPTZ NULL,
    lease_epoch   BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    attempts      INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts  INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 20),
    last_error    TEXT NULL,
    available_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_work_item_dedupe ON work_item (version_id, type, dedupe_key);
CREATE INDEX idx_work_item_claim ON work_item (status, available_at, priority DESC, id);
CREATE INDEX idx_work_item_lease ON work_item (status, lease_until);
CREATE INDEX idx_work_item_version ON work_item (version_id, status, id);

CREATE TABLE work_item_attempt (
    id            BIGINT PRIMARY KEY,
    work_item_id  BIGINT NOT NULL REFERENCES work_item (id),
    attempt       INTEGER NOT NULL CHECK (attempt > 0),
    status        TEXT NOT NULL
                  CHECK (status IN ('claimed', 'running', 'done', 'failed', 'dead', 'recovered')),
    error         TEXT NULL,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ NULL,
    UNIQUE (work_item_id, attempt)
);

CREATE INDEX idx_work_item_attempt_item ON work_item_attempt (work_item_id, id);
