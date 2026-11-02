-- P7 delivery context: PDF metadata on report revisions (#36).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md §5 (#36) and
-- §4.2. PDF rendering never mutates the authoritative HTML: after publish only
-- pdf_ref/pdf_status/pdf_error/pdf_attempts/pdf_rendered_at/updated_at may
-- change; as_of/html_ref/input_fingerprint/template_version stay immutable.
-- Existing published revisions land as pdf_status='pending' so the P7 ensurer
-- can backfill their PDFs.

ALTER TABLE delivery_report_revision
    ADD COLUMN pdf_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (pdf_status IN ('pending', 'running', 'done', 'failed', 'disabled')),
    ADD COLUMN pdf_error TEXT NULL
        CHECK (pdf_error IS NULL OR char_length(pdf_error) <= 2000),
    ADD COLUMN pdf_attempts INTEGER NOT NULL DEFAULT 0 CHECK (pdf_attempts >= 0),
    ADD COLUMN pdf_rendered_at TIMESTAMPTZ NULL;

CREATE INDEX idx_delivery_report_revision_pdf_pending
    ON delivery_report_revision (pdf_status)
    WHERE pdf_status IN ('pending', 'failed');
