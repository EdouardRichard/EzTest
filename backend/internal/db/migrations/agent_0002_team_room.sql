-- P1 agent context: structured team-room posts and bounded digest versions.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md §3.2.

CREATE TABLE team_room_post (
    id            BIGINT PRIMARY KEY,
    version_id    BIGINT NOT NULL,
    team          TEXT NOT NULL CHECK (team IN ('clarification', 'design', 'execution', 'delivery', 'steward')),
    kind          TEXT NOT NULL CHECK (kind IN ('finding', 'decision', 'blocker', 'handoff', 'status')),
    content       TEXT NOT NULL CHECK (content <> ''),
    refs          JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(refs) = 'array'),
    agent_run_id  BIGINT NULL,
    created_by    BIGINT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_team_room_post_team ON team_room_post (version_id, team, id DESC);
CREATE INDEX idx_team_room_post_created ON team_room_post (version_id, created_at);

CREATE TABLE team_room_digest (
    id              BIGINT PRIMARY KEY,
    version_id      BIGINT NOT NULL,
    team            TEXT NOT NULL CHECK (team IN ('clarification', 'design', 'execution', 'delivery', 'steward')),
    summary         TEXT NOT NULL,
    pinned_post_ids JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(pinned_post_ids) = 'array'),
    token_estimate  INTEGER NOT NULL DEFAULT 0 CHECK (token_estimate >= 0),
    version         INTEGER NOT NULL CHECK (version > 0),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (version_id, team, version)
);

CREATE INDEX idx_team_room_digest_latest ON team_room_digest (version_id, team, version DESC);
