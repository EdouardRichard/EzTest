-- P5 visual context: visual assertion config, approved baseline history and
-- per-comparison diffs (#28). See
-- docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md §3.5.
-- visual_assertion is mutable config (config_revision++); visual_baseline is an
-- approved history (old approved rows become superseded); visual_diff is
-- append-only: updated_at is initialized on insert and never updated (D34).
-- evidence_id/actual_evidence_id/diff_evidence_id/case_run_id/
-- assertion_result_id are logical references to execution/storage (no FK).

CREATE TABLE visual_assertion (
    id                      BIGINT NOT NULL,
    version_id              BIGINT NOT NULL,
    case_version_id         BIGINT NOT NULL,
    case_assertion_id       BIGINT NOT NULL,
    step_seq                INTEGER NOT NULL CHECK (step_seq > 0),
    assertion_seq           INTEGER NOT NULL CHECK (assertion_seq > 0),
    version_sut_id          BIGINT NOT NULL,
    selector                TEXT NOT NULL DEFAULT '' CHECK (char_length(selector) <= 500),
    region                  JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(region) = 'object'),
    threshold               NUMERIC(6, 5) NOT NULL DEFAULT 0.005
                            CHECK (threshold >= 0 AND threshold <= 0.1),
    viewport_width          INTEGER NOT NULL DEFAULT 1280 CHECK (viewport_width BETWEEN 1 AND 10000),
    viewport_height         INTEGER NOT NULL DEFAULT 720 CHECK (viewport_height BETWEEN 1 AND 10000),
    device_scale_factor     NUMERIC(4, 2) NOT NULL DEFAULT 1.0
                            CHECK (device_scale_factor >= 0.5 AND device_scale_factor <= 4),
    wait_policy             JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(wait_policy) = 'object'),
    config_revision         INTEGER NOT NULL DEFAULT 1 CHECK (config_revision >= 1),
    status                  TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by              BIGINT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_visual_assertion PRIMARY KEY (id),
    CONSTRAINT uk_visual_assertion_case_assertion UNIQUE (case_assertion_id)
);

CREATE INDEX idx_visual_assertion_version ON visual_assertion (version_id, status);
CREATE INDEX idx_visual_assertion_case_version ON visual_assertion (case_version_id);
CREATE INDEX idx_visual_assertion_version_sut ON visual_assertion (version_sut_id);

CREATE TABLE visual_baseline (
    id                      BIGINT NOT NULL,
    visual_assertion_id     BIGINT NOT NULL REFERENCES visual_assertion (id) ON DELETE CASCADE,
    case_version_id         BIGINT NOT NULL,
    version_sut_id          BIGINT NOT NULL,
    evidence_id             BIGINT NOT NULL,
    sha256                  TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes              BIGINT NOT NULL CHECK (size_bytes > 0),
    width                   INTEGER NOT NULL CHECK (width > 0),
    height                  INTEGER NOT NULL CHECK (height > 0),
    viewport_width          INTEGER NOT NULL CHECK (viewport_width > 0),
    viewport_height         INTEGER NOT NULL CHECK (viewport_height > 0),
    device_scale_factor     NUMERIC(4, 2) NOT NULL
                            CHECK (device_scale_factor >= 0.5 AND device_scale_factor <= 4),
    config_revision         INTEGER NOT NULL CHECK (config_revision >= 1),
    environment_fingerprint TEXT NOT NULL CHECK (environment_fingerprint ~ '^[0-9a-f]{64}$'),
    status                  TEXT NOT NULL DEFAULT 'approved'
                            CHECK (status IN ('approved', 'superseded')),
    approved_by             BIGINT NOT NULL,
    approved_at             TIMESTAMPTZ NOT NULL,
    superseded_at           TIMESTAMPTZ NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_visual_baseline PRIMARY KEY (id)
);

-- At most one approved baseline per assertion; approving a new one supersedes
-- the previous approved row in the same transaction.
CREATE UNIQUE INDEX uk_visual_baseline_approved
    ON visual_baseline (visual_assertion_id) WHERE status = 'approved';
CREATE INDEX idx_visual_baseline_assertion ON visual_baseline (visual_assertion_id, id DESC);
CREATE INDEX idx_visual_baseline_evidence ON visual_baseline (evidence_id);

CREATE TABLE visual_diff (
    id                      BIGINT NOT NULL,
    visual_assertion_id     BIGINT NOT NULL REFERENCES visual_assertion (id) ON DELETE CASCADE,
    case_run_id             BIGINT NOT NULL,
    assertion_result_id     BIGINT NOT NULL,
    baseline_id             BIGINT NULL REFERENCES visual_baseline (id) ON DELETE SET NULL,
    actual_evidence_id      BIGINT NOT NULL,
    diff_evidence_id        BIGINT NULL,
    diff_ratio              NUMERIC(8, 6) NOT NULL DEFAULT 0
                            CHECK (diff_ratio >= 0 AND diff_ratio <= 1),
    threshold               NUMERIC(6, 5) NOT NULL
                            CHECK (threshold >= 0 AND threshold <= 0.1),
    classification          TEXT NOT NULL
                            CHECK (classification IN ('passed', 'failed', 'inconclusive', 'blocked')),
    reason                  TEXT NOT NULL DEFAULT '' CHECK (char_length(reason) <= 2000),
    environment_match       BOOLEAN NOT NULL DEFAULT true,
    width                   INTEGER NOT NULL DEFAULT 0 CHECK (width >= 0),
    height                  INTEGER NOT NULL DEFAULT 0 CHECK (height >= 0),
    mismatch_boxes          JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(mismatch_boxes) = 'array'),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_visual_diff PRIMARY KEY (id),
    CONSTRAINT uk_visual_diff_assertion_result UNIQUE (assertion_result_id)
);

CREATE INDEX idx_visual_diff_run ON visual_diff (case_run_id, id DESC);
CREATE INDEX idx_visual_diff_assertion ON visual_diff (visual_assertion_id, id DESC);
CREATE INDEX idx_visual_diff_classification ON visual_diff (classification, created_at DESC);
