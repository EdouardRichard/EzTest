-- P1 version context: requirement attachments backed by the content ledger.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md §3.6.

CREATE TABLE requirement_attachment (
    id                       BIGINT PRIMARY KEY,
    version_id               BIGINT NOT NULL,
    requirement_id           BIGINT NULL,
    requirement_version_id   BIGINT NULL,
    conversation_message_id  BIGINT NULL,
    content_ref_id           BIGINT NOT NULL,
    name                     TEXT NOT NULL CHECK (name <> ''),
    mime                     TEXT NOT NULL CHECK (mime <> ''),
    size                     BIGINT NOT NULL CHECK (size >= 0),
    extraction_status        TEXT NOT NULL DEFAULT 'pending'
                             CHECK (extraction_status IN ('pending', 'processing', 'done', 'failed', 'skipped')),
    extracted_content_ref_id BIGINT NULL,
    extraction_error         TEXT NULL,
    created_by               BIGINT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_requirement_attachment_version ON requirement_attachment (version_id, requirement_id);
CREATE INDEX idx_requirement_attachment_message ON requirement_attachment (conversation_message_id);
CREATE INDEX idx_requirement_attachment_pending ON requirement_attachment (extraction_status, id);
