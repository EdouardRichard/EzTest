CREATE TABLE IF NOT EXISTS agent_profiles (
    id                   BIGSERIAL PRIMARY KEY,
    key                  TEXT NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9_-]*$'),
    name                 TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    builtin              BOOLEAN NOT NULL DEFAULT false,
    enabled              BOOLEAN NOT NULL DEFAULT true,
    system_prompt        TEXT NOT NULL,
    model                TEXT NOT NULL DEFAULT '',             -- '' = 用默认 LLM 配置的 model
    allowed_tools        TEXT[] NOT NULL DEFAULT '{}',
    max_turns            INT NOT NULL DEFAULT 0 CHECK (max_turns BETWEEN 0 AND 100),   -- 0 = 不限
    run_seconds          INT NOT NULL DEFAULT 600 CHECK (run_seconds BETWEEN 30 AND 3600),
    hard_timeout_seconds INT NOT NULL DEFAULT 60 CHECK (hard_timeout_seconds BETWEEN 5 AND 300),
    thinking             TEXT NOT NULL DEFAULT 'enabled'
                         CHECK (thinking IN ('auto','enabled','disabled')),
    reasoning_effort     TEXT NOT NULL DEFAULT 'max'
                         CHECK (reasoning_effort IN ('','none','low','high','max')),
    max_output_tokens    INT NOT NULL DEFAULT 32768 CHECK (max_output_tokens BETWEEN 1024 AND 131072),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id               BIGSERIAL PRIMARY KEY,
    profile_id       BIGINT NOT NULL REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    title            TEXT NOT NULL DEFAULT '',
    created_by       BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_created ON agent_sessions(created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS agent_runs (
    id                   BIGSERIAL PRIMARY KEY,
    session_id           BIGINT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    status               TEXT NOT NULL DEFAULT 'running'
                         CHECK (status IN ('running','done','failed','aborted','interrupted')),
    terminal_reason      TEXT CHECK (terminal_reason IN (
                           'completed','max_turns','timeout','model_error','context_overflow',
                           'aborted_streaming','aborted_tools','aborted','shutdown',
                           'run_hard_timeout','interrupted')),
    abort_code           TEXT,
    abort_message        TEXT,
    error_code           TEXT,
    error_message        TEXT,
    turns                INT NOT NULL DEFAULT 0,
    input_tokens         BIGINT NOT NULL DEFAULT 0,
    output_tokens        BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens     BIGINT NOT NULL DEFAULT 0,
    last_tool_name       TEXT,
    last_tool_pending    BOOLEAN NOT NULL DEFAULT false,
    started_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at             TIMESTAMPTZ
);
-- 会话内单飞：同一会话最多一个 running run
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_runs_running
    ON agent_runs(session_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_agent_runs_session ON agent_runs(session_id, id DESC);

CREATE TABLE IF NOT EXISTS agent_messages (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    run_id       BIGINT REFERENCES agent_runs(id) ON DELETE SET NULL,
    role         TEXT NOT NULL CHECK (role IN ('system','user','assistant','tool')),
    content      JSONB NOT NULL DEFAULT '[]',    -- parts 数组，与 P0 llm.Message 同构
    reasoning    TEXT NOT NULL DEFAULT '',       -- 思考链；请求带 tools 时必须回传
    tool_calls   JSONB,                          -- assistant 的工具调用数组
    tool_call_id TEXT,                           -- tool 消息对应调用
    tool_name    TEXT,
    usage        JSONB,                          -- {prompt_tokens,completion_tokens,reasoning_tokens,total_tokens}
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_messages_session ON agent_messages(session_id, id);
CREATE INDEX IF NOT EXISTS idx_agent_messages_run ON agent_messages(run_id, id);
