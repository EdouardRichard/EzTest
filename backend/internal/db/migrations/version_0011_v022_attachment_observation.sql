-- v0.22 E attachment observation schema: structured extraction coverage on
-- requirement_attachment and one row per image extracted from a document
-- attachment. v0.22 rebuilds the database from scratch, so parse_status is
-- removed rather than migrated: extraction_status stays the single attachment
-- status and extraction_report carries the found/parsed/skipped coverage plus
-- skip reasons (spec DB-18, C-1, E §2.3).
--
-- content_ref_id/attachment_id stay logical BIGINT references (content ledger /
-- version context, Agent.md §6.9.7), so no cross-context foreign keys.
-- See docs/superpowers/specs/2026-11-05-eztest-v0.22-design.md §5 DB-17/DB-18,
-- §4.7 R-A7-3a and docs/version-V0.22/E-tools-attachments.md §2.3.

ALTER TABLE requirement_attachment
    ADD COLUMN extraction_report JSONB NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT chk_requirement_attachment_extraction_report
        CHECK (jsonb_typeof(extraction_report) = 'object');

-- The 0.2.1 parse_status column duplicated extraction_status and was never
-- read; dropping the column also drops its inline status CHECK.
ALTER TABLE requirement_attachment DROP COLUMN parse_status;

CREATE TABLE attachment_image (
    id              BIGINT NOT NULL,
    attachment_id   BIGINT NOT NULL,
    seq             INTEGER NOT NULL
                    CONSTRAINT chk_attachment_image_seq CHECK (seq > 0),
    content_ref_id  BIGINT NULL,
    mime            TEXT NOT NULL DEFAULT '',
    width           INTEGER NULL
                    CONSTRAINT chk_attachment_image_width
                    CHECK (width IS NULL OR width > 0),
    height          INTEGER NULL
                    CONSTRAINT chk_attachment_image_height
                    CHECK (height IS NULL OR height > 0),
    source_kind     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'extracted'
                    CONSTRAINT chk_attachment_image_status
                    CHECK (status IN ('extracted', 'analyzing', 'done', 'failed', 'skipped')),
    caption         TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_attachment_image PRIMARY KEY (id),
    CONSTRAINT uk_attachment_image_attachment_seq UNIQUE (attachment_id, seq)
);

-- Status polling by attachment (message chips, image listing, reanalyze).
CREATE INDEX idx_attachment_image_attachment_status
    ON attachment_image (attachment_id, status);
