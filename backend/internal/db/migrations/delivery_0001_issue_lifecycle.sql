-- P4 delivery context: issue, issue references and status history (#21).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p4-delivery-chain-design.md §3.2.
-- Same-context FKs are explicit; version/identity/agent_run references are
-- logical BIGINT only. IDs are application-generated snowflakes (D29).
-- issue_ref and issue_status_history are append-only: updated_at is initialized
-- on insert and never updated by the repository (D34 §6.9.4).

CREATE TABLE delivery_issue (
    id                  BIGINT NOT NULL,
    version_id          BIGINT NOT NULL,
    title               TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 500),
    summary             TEXT NOT NULL DEFAULT '' CHECK (char_length(summary) <= 4000),
    status              TEXT NOT NULL DEFAULT 'open'
                        CHECK (status IN ('open', 'confirmed', 'dismissed', 'merged')),
    category            TEXT NOT NULL CHECK (category IN ('ui', 'api', 'db')),
    severity            TEXT NOT NULL CHECK (severity IN ('major', 'minor', 'trivial')),
    dedupe_key          TEXT NOT NULL CHECK (dedupe_key ~ '^[0-9a-f]{32,64}$'),
    fingerprint         TEXT NOT NULL DEFAULT ''
                        CHECK (fingerprint = '' OR fingerprint ~ '^[0-9a-f]{64}$'),
    source              TEXT NOT NULL DEFAULT 'failure_candidate'
                        CHECK (source IN ('failure_candidate')),
    expected_revision   INTEGER NOT NULL DEFAULT 1 CHECK (expected_revision > 0),
    merged_into_id      BIGINT NULL REFERENCES delivery_issue (id),
    merged_at           TIMESTAMPTZ NULL,
    merged_by           BIGINT NULL,
    reopen_count        INTEGER NOT NULL DEFAULT 0 CHECK (reopen_count >= 0),
    created_by_run_id   BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_delivery_issue PRIMARY KEY (id),
    CONSTRAINT uk_delivery_issue_version_id_dedupe_key UNIQUE (version_id, dedupe_key),
    CHECK (merged_into_id IS NULL OR merged_into_id <> id)
);

CREATE INDEX idx_delivery_issue_version_id_status_id
    ON delivery_issue (version_id, status, id DESC);
CREATE INDEX idx_delivery_issue_merged_into_id ON delivery_issue (merged_into_id);
CREATE INDEX idx_delivery_issue_created_at ON delivery_issue (created_at);

CREATE TABLE delivery_issue_ref (
    id              BIGINT NOT NULL,
    issue_id        BIGINT NOT NULL REFERENCES delivery_issue (id) ON DELETE CASCADE,
    kind            TEXT NOT NULL
                    CHECK (kind IN ('case_run', 'case_version', 'wave', 'step_result',
                                    'assertion_result', 'evidence', 'report', 'requirement')),
    ref_id          BIGINT NOT NULL,
    version_sut_id  BIGINT NULL,
    role            TEXT NOT NULL DEFAULT 'source' CHECK (char_length(role) <= 32),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_delivery_issue_ref PRIMARY KEY (id),
    CONSTRAINT uk_delivery_issue_ref_issue_id_kind_ref_id_role
        UNIQUE (issue_id, kind, ref_id, role)
);

CREATE INDEX idx_delivery_issue_ref_kind_ref_id ON delivery_issue_ref (kind, ref_id);
CREATE INDEX idx_delivery_issue_ref_issue_id_id ON delivery_issue_ref (issue_id, id);

CREATE TABLE delivery_issue_status_history (
    id          BIGINT NOT NULL,
    issue_id    BIGINT NOT NULL REFERENCES delivery_issue (id) ON DELETE CASCADE,
    from_status TEXT NULL
                CHECK (from_status IS NULL OR from_status IN ('open', 'confirmed', 'dismissed', 'merged')),
    to_status   TEXT NOT NULL
                CHECK (to_status IN ('open', 'confirmed', 'dismissed', 'merged')),
    reason      TEXT NOT NULL DEFAULT '' CHECK (char_length(reason) <= 2000),
    actor_id    BIGINT NULL,
    actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user', 'agent', 'system')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_delivery_issue_status_history PRIMARY KEY (id)
);

CREATE INDEX idx_delivery_issue_status_history_issue_id_id
    ON delivery_issue_status_history (issue_id, id);
