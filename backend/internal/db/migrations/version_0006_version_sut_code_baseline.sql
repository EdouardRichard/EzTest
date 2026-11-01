-- P5 version context: optional code baseline commit on version_sut (#30).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md §3.7.
-- Additive column only. The commit is resolved when the version binding is
-- created/updated; a resolution failure keeps NULL and the analysis API reports
-- baseline_commit_missing (422). No new table.

ALTER TABLE version_sut ADD COLUMN code_baseline_commit TEXT NULL
    CHECK (code_baseline_commit IS NULL OR code_baseline_commit ~ '^[0-9a-f]{40}$');
