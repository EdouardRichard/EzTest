-- P1 agent context: profiles, runs, LLM transcript and per-run context packets.
-- See docs/superpowers/specs/2026-10-31-eztest-v02-p1-runtime-clarification-design.md §3.3.

CREATE TABLE agent_profile (
    id            BIGINT PRIMARY KEY,
    key           TEXT NOT NULL UNIQUE CHECK (key <> ''),
    name          TEXT NOT NULL CHECK (name <> ''),
    system_prompt TEXT NOT NULL CHECK (system_prompt <> ''),
    allowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_tools) = 'array'),
    model         TEXT NULL,
    budgets       JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(budgets) = 'object'),
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_run (
    id                BIGINT PRIMARY KEY,
    profile_key       TEXT NOT NULL,
    work_item_id      BIGINT NULL,
    conversation_id   BIGINT NULL,
    status            TEXT NOT NULL DEFAULT 'running'
                      CHECK (status IN ('running', 'done', 'failed', 'aborted', 'interrupted')),
    terminal_reason   TEXT NULL,
    turns             INTEGER NOT NULL DEFAULT 0 CHECK (turns >= 0),
    usage             JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(usage) = 'object'),
    owner_instance    TEXT NOT NULL CHECK (owner_instance <> ''),
    lease_epoch       BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    context_packet_id BIGINT NULL,
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at       TIMESTAMPTZ NULL
);

CREATE INDEX idx_agent_run_work_item ON agent_run (work_item_id);
CREATE INDEX idx_agent_run_conversation ON agent_run (conversation_id, id DESC);
CREATE INDEX idx_agent_run_status ON agent_run (status);

CREATE TABLE agent_message (
    id              BIGINT PRIMARY KEY,
    run_id          BIGINT NULL,
    conversation_id BIGINT NULL,
    role            TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
    parts           JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(parts) = 'array'),
    tool_calls      JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(tool_calls) = 'array'),
    tool_call_id    TEXT NULL,
    usage           JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(usage) = 'object'),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_message_run ON agent_message (run_id, id);
CREATE INDEX idx_agent_message_conversation ON agent_message (conversation_id, id);

CREATE TABLE context_packet (
    id           BIGINT PRIMARY KEY,
    run_id       BIGINT NOT NULL,
    version_id   BIGINT NOT NULL,
    sections     JSONB NOT NULL CHECK (jsonb_typeof(sections) = 'array'),
    token_counts JSONB NOT NULL CHECK (jsonb_typeof(token_counts) = 'object'),
    hash         TEXT NOT NULL CHECK (hash ~ '^[0-9a-f]{64}$'),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_context_packet_run ON context_packet (run_id);
