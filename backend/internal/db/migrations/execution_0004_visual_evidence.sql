-- P5 execution context: extend evidence.kind with visual_actual/visual_diff (#29).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md §3.6.
-- Additive only: no column changes and no existing evidence semantics change.
-- The old check was created inline on evidence.kind, so PostgreSQL auto-named it
-- evidence_kind_check; DROP IF EXISTS keeps this migration safe on a fresh DB.
-- New constraint name follows the P5 contract: chk_evidence_kind.

ALTER TABLE evidence DROP CONSTRAINT IF EXISTS evidence_kind_check;
ALTER TABLE evidence ADD CONSTRAINT chk_evidence_kind
    CHECK (kind IN ('screenshot', 'snapshot', 'console', 'network', 'http', 'db',
                    'download', 'text', 'visual_actual', 'visual_diff'));
