-- P1 version context: clarification conversations, messages and rolling summaries.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md §3.5.

CREATE TABLE conversation (
    id          BIGINT PRIMARY KEY,
    version_id  BIGINT NOT NULL REFERENCES version (id),
    kind        TEXT NOT NULL CHECK (kind IN ('clarification', 'design')),
    title       TEXT NOT NULL CHECK (title <> ''),
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
    created_by  BIGINT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_conversation_version_kind ON conversation (version_id, kind, id DESC);

CREATE TABLE conversation_message (
    id                  BIGINT PRIMARY KEY,
    conversation_id     BIGINT NOT NULL REFERENCES conversation (id),
    role                TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    parts               JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(parts) = 'array'),
    refs                JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(refs) = 'array'),
    token_usage         JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(token_usage) = 'object'),
    actor_id            BIGINT NULL,
    agent_run_id        BIGINT NULL,
    client_message_id   TEXT NULL CHECK (client_message_id IS NULL OR char_length(client_message_id) <= 64),
    reply_to_message_id BIGINT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, client_message_id)
);

CREATE INDEX idx_conversation_message_conversation ON conversation_message (conversation_id, id);
CREATE INDEX idx_conversation_message_reply ON conversation_message (conversation_id, reply_to_message_id);

CREATE TABLE conversation_summary (
    id              BIGINT PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES conversation (id),
    summary         TEXT NOT NULL,
    pinned_facts    JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(pinned_facts) = 'array'),
    token_estimate  INTEGER NOT NULL DEFAULT 0 CHECK (token_estimate >= 0),
    version         INTEGER NOT NULL CHECK (version > 0),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, version)
);

CREATE INDEX idx_conversation_summary_latest ON conversation_summary (conversation_id, version DESC);
