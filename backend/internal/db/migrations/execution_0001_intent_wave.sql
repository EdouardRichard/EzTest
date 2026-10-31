-- P3 execution context: execution intent / run wave / case run / per-SUT context.
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md §3.1.
-- Same-context FKs are explicit; version/case/version_sut/content references are
-- logical only (no cross-context FK). IDs are application-generated snowflakes.

CREATE TABLE execution_intent (
    id                      BIGINT NOT NULL,
    version_id              BIGINT NOT NULL,
    case_version_id         BIGINT NOT NULL,
    version_sut_ids         JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(version_sut_ids) = 'array'),
    environment_fingerprint TEXT NOT NULL CHECK (environment_fingerprint ~ '^[0-9a-f]{64}$'),
    code_baseline           TEXT NOT NULL DEFAULT '' CHECK (char_length(code_baseline) <= 256),
    purpose                 TEXT NOT NULL DEFAULT 'initial'
                            CHECK (purpose IN ('initial', 'retest', 'manual')),
    status                  TEXT NOT NULL DEFAULT 'open'
                            CHECK (status IN ('open', 'claimed', 'running', 'done', 'failed', 'cancelled', 'superseded')),
    dedupe_key              TEXT NOT NULL CHECK (char_length(dedupe_key) BETWEEN 1 AND 256),
    attempt_no              INTEGER NOT NULL DEFAULT 1 CHECK (attempt_no >= 1),
    created_by              BIGINT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_execution_intent PRIMARY KEY (id)
);

-- At most one active intent per idempotency key (P3-D-08); retests create new
-- rows after the previous row leaves the open/claimed/running set.
CREATE UNIQUE INDEX uk_execution_intent_active
    ON execution_intent (case_version_id, environment_fingerprint, code_baseline, purpose)
    WHERE status IN ('open', 'claimed', 'running');

CREATE INDEX idx_execution_intent_version_status
    ON execution_intent (version_id, status, id DESC);
CREATE INDEX idx_execution_intent_case_version
    ON execution_intent (case_version_id, status);
CREATE INDEX idx_execution_intent_dedupe
    ON execution_intent (version_id, dedupe_key);

CREATE TABLE run_wave (
    id                  BIGINT NOT NULL,
    version_id          BIGINT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'planned'
                        CHECK (status IN ('planned', 'running', 'paused', 'done', 'failed', 'cancelled')),
    trigger             TEXT NOT NULL DEFAULT 'auto'
                        CHECK (trigger IN ('auto', 'manual', 'retest')),
    counts              JSONB NOT NULL DEFAULT '{}'::jsonb
                        CHECK (jsonb_typeof(counts) = 'object'),
    started_at          TIMESTAMPTZ NULL,
    finished_at         TIMESTAMPTZ NULL,
    paused_at           TIMESTAMPTZ NULL,
    paused_by           BIGINT NULL,
    pause_reason        TEXT NOT NULL DEFAULT '' CHECK (char_length(pause_reason) <= 200),
    pause_mode          TEXT NOT NULL DEFAULT ''
                        CHECK (pause_mode IN ('', 'graceful', 'immediate')),
    report_id           BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_run_wave PRIMARY KEY (id)
);

-- Single active wave per version: planned/running/paused all count as active
-- (P3-D-10); paused keeps the slot.
CREATE UNIQUE INDEX uk_run_wave_active
    ON run_wave (version_id)
    WHERE status IN ('planned', 'running', 'paused');

CREATE INDEX idx_run_wave_version ON run_wave (version_id, id DESC);
CREATE INDEX idx_run_wave_status ON run_wave (status, id);

CREATE TABLE case_run (
    id                  BIGINT NOT NULL,
    wave_id             BIGINT NOT NULL REFERENCES run_wave (id),
    intent_id           BIGINT NOT NULL REFERENCES execution_intent (id),
    case_version_id     BIGINT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'running', 'paused', 'passed', 'failed',
                                          'blocked', 'inconclusive', 'skipped', 'interrupted', 'superseded')),
    attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    failure_class       TEXT NOT NULL DEFAULT ''
                        CHECK (failure_class IN ('', 'application', 'environment', 'session', 'model',
                                                 'automation', 'timeout', 'executor_lost', 'blocked', 'unknown')),
    summary             JSONB NOT NULL DEFAULT '{}'::jsonb
                        CHECK (jsonb_typeof(summary) = 'object'),
    started_at          TIMESTAMPTZ NULL,
    finished_at         TIMESTAMPTZ NULL,
    owner_instance      TEXT NOT NULL DEFAULT '',
    owner_executor_id   BIGINT NULL,
    dispatch_epoch      BIGINT NOT NULL DEFAULT 0 CHECK (dispatch_epoch >= 0),
    dispatch_token      TEXT NOT NULL DEFAULT '' CHECK (char_length(dispatch_token) <= 128),
    lease_until         TIMESTAMPTZ NULL,
    paused_at           TIMESTAMPTZ NULL,
    paused_by           BIGINT NULL,
    pause_reason        TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_run PRIMARY KEY (id),
    CONSTRAINT uk_case_run_wave_intent UNIQUE (wave_id, intent_id)
);

CREATE INDEX idx_case_run_wave ON case_run (wave_id, status, id);
CREATE INDEX idx_case_run_intent ON case_run (intent_id);
CREATE INDEX idx_case_run_case_version ON case_run (case_version_id);
CREATE INDEX idx_case_run_lease ON case_run (status, lease_until);

CREATE TABLE case_run_sut_context (
    id                  BIGINT NOT NULL,
    case_run_id         BIGINT NOT NULL REFERENCES case_run (id),
    version_sut_id      BIGINT NOT NULL,
    account_role        TEXT NOT NULL DEFAULT '' CHECK (char_length(account_role) <= 100),
    account_id          BIGINT NULL,
    browser_context_id  TEXT NOT NULL DEFAULT '' CHECK (char_length(browser_context_id) <= 128),
    storage_state       JSONB NULL CHECK (storage_state IS NULL OR jsonb_typeof(storage_state) = 'object'),
    lease_epoch         BIGINT NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'creating'
                        CHECK (status IN ('creating', 'active', 'closed', 'failed')),
    closed_at           TIMESTAMPTZ NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_case_run_sut_context PRIMARY KEY (id),
    CONSTRAINT uk_case_run_sut_context UNIQUE (case_run_id, version_sut_id, account_role)
);

CREATE INDEX idx_case_run_sut_context_run ON case_run_sut_context (case_run_id);
CREATE INDEX idx_case_run_sut_context_context ON case_run_sut_context (browser_context_id);
