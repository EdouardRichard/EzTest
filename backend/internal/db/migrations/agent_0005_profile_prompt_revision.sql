-- P6 agent context: append-only profile prompt revisions (#33).
-- See docs/superpowers/specs/2026-11-01-eztest-v02-p6-parity-quality-search-agent-center-design.md §3.3.
-- agent_profile.system_prompt stays the runtime source of truth; the current
-- version is max(version). Revisions are seeded/backfilled by EnsureProfiles.

CREATE TABLE agent_profile_prompt_revision (
    id           BIGINT PRIMARY KEY,
    profile_key  TEXT NOT NULL REFERENCES agent_profile(key) ON DELETE CASCADE,
    version      INT NOT NULL CHECK (version > 0),
    system_prompt TEXT NOT NULL CHECK (system_prompt <> ''),
    note         TEXT NULL,
    source       TEXT NOT NULL CHECK (source IN ('seed','admin','reset')),
    actor_id     BIGINT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_agent_profile_prompt_revision UNIQUE (profile_key, version)
);
CREATE INDEX idx_agent_profile_prompt_revision_key
    ON agent_profile_prompt_revision (profile_key, version DESC);
