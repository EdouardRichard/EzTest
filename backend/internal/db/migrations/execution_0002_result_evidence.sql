-- P3 execution context: step results / assertion results / immutable evidence /
-- evidence ownership links. See
-- docs/superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md §3.2.
-- Evidence bytes live in the P0 content ledger (content_ref_id, logical
-- reference); this table only stores metadata. evidence is immutable: it has
-- created_at but no updated_at and no update/delete API (P3-D-20).

CREATE TABLE step_result (
    id                      BIGINT NOT NULL,
    case_run_id             BIGINT NOT NULL REFERENCES case_run (id),
    seq                     INTEGER NOT NULL CHECK (seq > 0),
    target_version_sut_id   BIGINT NULL,
    status                  TEXT NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'running', 'passed', 'failed', 'blocked', 'skipped')),
    observed                JSONB NOT NULL DEFAULT '{}'::jsonb
                            CHECK (jsonb_typeof(observed) = 'object'),
    error                   TEXT NOT NULL DEFAULT '' CHECK (char_length(error) <= 2000),
    started_at              TIMESTAMPTZ NULL,
    finished_at             TIMESTAMPTZ NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_step_result PRIMARY KEY (id),
    CONSTRAINT uk_step_result_run_seq UNIQUE (case_run_id, seq)
);

CREATE INDEX idx_step_result_run ON step_result (case_run_id, seq);

CREATE TABLE assertion_result (
    id                      BIGINT NOT NULL,
    case_run_id             BIGINT NOT NULL REFERENCES case_run (id),
    step_seq                INTEGER NOT NULL CHECK (step_seq > 0),
    assertion_seq           INTEGER NOT NULL CHECK (assertion_seq > 0),
    kind                    TEXT NOT NULL
                            CHECK (kind IN ('ui', 'api', 'db', 'text', 'visual')),
    verdict                 TEXT NOT NULL
                            CHECK (verdict IN ('passed', 'failed', 'inconclusive', 'skipped')),
    reason                  TEXT NOT NULL DEFAULT '' CHECK (char_length(reason) <= 2000),
    expected                TEXT NOT NULL DEFAULT '' CHECK (char_length(expected) <= 4000),
    actual                  TEXT NOT NULL DEFAULT '' CHECK (char_length(actual) <= 4000),
    target_version_sut_id   BIGINT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_assertion_result PRIMARY KEY (id),
    CONSTRAINT uk_assertion_result_run_step_assertion UNIQUE (case_run_id, step_seq, assertion_seq)
);

CREATE INDEX idx_assertion_result_run
    ON assertion_result (case_run_id, step_seq, assertion_seq);

CREATE TABLE evidence (
    id                  BIGINT NOT NULL,
    case_run_id         BIGINT NOT NULL REFERENCES case_run (id),
    version_sut_id      BIGINT NULL,
    step_seq            INTEGER NULL CHECK (step_seq IS NULL OR step_seq > 0),
    assertion_seq       INTEGER NULL CHECK (assertion_seq IS NULL OR assertion_seq > 0),
    kind                TEXT NOT NULL
                        CHECK (kind IN ('screenshot', 'snapshot', 'console', 'network',
                                        'http', 'db', 'download', 'text')),
    content_ref_id      BIGINT NOT NULL,
    sha256              TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes          BIGINT NOT NULL CHECK (size_bytes >= 0),
    mime                TEXT NOT NULL DEFAULT 'application/octet-stream'
                        CHECK (char_length(mime) <= 128),
    filename            TEXT NOT NULL DEFAULT '' CHECK (char_length(filename) <= 255),
    created_by_run_id   BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_evidence PRIMARY KEY (id)
);

CREATE INDEX idx_evidence_case_run ON evidence (case_run_id, id DESC);
CREATE INDEX idx_evidence_version_sut ON evidence (version_sut_id, id DESC);
CREATE INDEX idx_evidence_content_ref ON evidence (content_ref_id);
CREATE INDEX idx_evidence_kind ON evidence (kind);

CREATE TABLE evidence_ref (
    id              BIGINT NOT NULL,
    evidence_id     BIGINT NOT NULL REFERENCES evidence (id),
    owner_kind      TEXT NOT NULL
                    CHECK (owner_kind IN ('case_run', 'step_result', 'assertion_result', 'human_request')),
    owner_id        BIGINT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'attachment' CHECK (char_length(role) <= 64),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_evidence_ref PRIMARY KEY (id),
    CONSTRAINT uk_evidence_ref_owner UNIQUE (evidence_id, owner_kind, owner_id, role)
);

CREATE INDEX idx_evidence_ref_owner ON evidence_ref (owner_kind, owner_id);
