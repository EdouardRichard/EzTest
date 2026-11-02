CREATE TABLE llm_config (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'openai',
    protocol TEXT NOT NULL DEFAULT 'openai' CHECK (protocol IN ('openai', 'openai_compatible')),
    base_url TEXT NOT NULL,
    api_key TEXT NOT NULL DEFAULT '',
    headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    timeout_seconds INT NOT NULL DEFAULT 60 CHECK (timeout_seconds BETWEEN 1 AND 600),
    max_retries INT NOT NULL DEFAULT 2 CHECK (max_retries BETWEEN 0 AND 10),
    is_default BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_by BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_llm_config_name ON llm_config (lower(name));
CREATE UNIQUE INDEX uk_llm_config_default ON llm_config (is_default) WHERE is_default;

CREATE TABLE llm_model (
    id BIGINT PRIMARY KEY,
    config_id BIGINT NOT NULL REFERENCES llm_config(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    context_window INT NOT NULL DEFAULT 0 CHECK (context_window >= 0),
    max_output INT NOT NULL DEFAULT 0 CHECK (max_output >= 0),
    supports_tools BOOLEAN NOT NULL DEFAULT false,
    supports_vision BOOLEAN NOT NULL DEFAULT false,
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('catalog', 'manual')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uk_llm_model_config_model ON llm_model (config_id, model);
