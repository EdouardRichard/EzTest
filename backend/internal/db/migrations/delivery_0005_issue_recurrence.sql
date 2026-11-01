-- P7 delivery context: issue recurrence counters (#39).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md §5 (#39) and
-- §4.6. When a merged issue's dedupe key recurs, the recurrence is redirected to
-- the merge target, which bumps recurrence_count/last_recurred_at; the source
-- issue keeps its historical refs unchanged.

ALTER TABLE delivery_issue
    ADD COLUMN recurrence_count INTEGER NOT NULL DEFAULT 0 CHECK (recurrence_count >= 0),
    ADD COLUMN last_recurred_at TIMESTAMPTZ NULL;
