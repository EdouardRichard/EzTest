-- P1: close the P0 handoff's event index drift without editing the frozen P0 migration.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md §3.7.

CREATE INDEX idx_event_channel_created_at ON event (channel, created_at);
