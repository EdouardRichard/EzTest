-- P4 delivery context: report, report revisions and revision references (#22).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p4-delivery-chain-design.md §3.3.
-- Same-context FKs are explicit; version/run_wave/content_ref/agent_run
-- references are logical BIGINT only. IDs are application-generated snowflakes
-- (D29). Report HTML bytes live in the P0 content ledger (html_ref =
-- content_ref.id, owner_kind 'report_revision', role 'report_html').
-- report_revision content is append-only after publish and report_ref is
-- append-only: their updated_at is initialized on insert only (D34 §6.9.4).

CREATE TABLE delivery_report (
    id                  BIGINT NOT NULL,
    version_id          BIGINT NOT NULL,
    wave_id             BIGINT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('wave', 'version')),
    current_revision_id BIGINT NULL,
    status              TEXT NOT NULL DEFAULT 'draft'
                        CHECK (status IN ('draft', 'published', 'stale')),
    input_fingerprint   TEXT NOT NULL DEFAULT ''
                        CHECK (input_fingerprint = '' OR input_fingerprint ~ '^[0-9a-f]{64}$'),
    created_by_run_id   BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_delivery_report PRIMARY KEY (id),
    CHECK ((kind = 'version' AND wave_id IS NULL) OR (kind = 'wave' AND wave_id IS NOT NULL))
);

-- One wave report per wave and one version report per version.
CREATE UNIQUE INDEX uk_delivery_report_version_id_wave_id
    ON delivery_report (version_id, wave_id)
    WHERE kind = 'wave';
CREATE UNIQUE INDEX uk_delivery_report_version_id_kind
    ON delivery_report (version_id)
    WHERE kind = 'version';
CREATE INDEX idx_delivery_report_version_id_kind_id
    ON delivery_report (version_id, kind, id DESC);
CREATE INDEX idx_delivery_report_current_revision_id ON delivery_report (current_revision_id);

CREATE TABLE delivery_report_revision (
    id                  BIGINT NOT NULL,
    report_id           BIGINT NOT NULL REFERENCES delivery_report (id) ON DELETE CASCADE,
    revision            INTEGER NOT NULL CHECK (revision > 0),
    status              TEXT NOT NULL DEFAULT 'draft'
                        CHECK (status IN ('draft', 'published', 'stale')),
    html_ref            BIGINT NULL,
    pdf_ref             BIGINT NULL,
    input_fingerprint   TEXT NOT NULL CHECK (input_fingerprint ~ '^[0-9a-f]{64}$'),
    as_of               TIMESTAMPTZ NOT NULL,
    template_version    TEXT NOT NULL DEFAULT '1' CHECK (char_length(template_version) <= 32),
    render_status       TEXT NOT NULL DEFAULT 'pending'
                        CHECK (render_status IN ('pending', 'running', 'done', 'failed')),
    created_by_run_id   BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_delivery_report_revision PRIMARY KEY (id),
    CONSTRAINT uk_delivery_report_revision_report_id_revision UNIQUE (report_id, revision)
);

CREATE INDEX idx_delivery_report_revision_report_id_revision
    ON delivery_report_revision (report_id, revision DESC);
CREATE INDEX idx_delivery_report_revision_html_ref_id ON delivery_report_revision (html_ref);

CREATE TABLE delivery_report_ref (
    id                  BIGINT NOT NULL,
    report_id           BIGINT NOT NULL REFERENCES delivery_report (id) ON DELETE CASCADE,
    report_revision_id  BIGINT NOT NULL REFERENCES delivery_report_revision (id) ON DELETE CASCADE,
    kind                TEXT NOT NULL
                        CHECK (kind IN ('requirement', 'scenario', 'case_version', 'wave',
                                        'case_run', 'issue', 'version_sut')),
    ref_id              BIGINT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_delivery_report_ref PRIMARY KEY (id),
    CONSTRAINT uk_delivery_report_ref_report_revision_id_kind_ref_id
        UNIQUE (report_revision_id, kind, ref_id)
);

CREATE INDEX idx_delivery_report_ref_kind_ref_id ON delivery_report_ref (kind, ref_id);
CREATE INDEX idx_delivery_report_ref_report_id_id ON delivery_report_ref (report_id, id);
