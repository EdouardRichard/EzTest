CREATE TABLE llm_configs (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  protocol TEXT NOT NULL CHECK (protocol IN ('chat_completions','responses')),
  base_url TEXT NOT NULL,
  model TEXT NOT NULL,
  api_key_encrypted BYTEA,
  key_version INT NOT NULL DEFAULT 1,
  headers_encrypted BYTEA,
  retry_count INT NOT NULL DEFAULT 2 CHECK (retry_count BETWEEN 0 AND 5),
  retry_interval_ms INT NOT NULL DEFAULT 500 CHECK (retry_interval_ms BETWEEN 100 AND 30000),
  timeout_ms INT NOT NULL DEFAULT 60000 CHECK (timeout_ms BETWEEN 1000 AND 300000),
  stream_enabled BOOLEAN NOT NULL DEFAULT true,
  multimodal_enabled BOOLEAN NOT NULL DEFAULT true,
  is_default BOOLEAN NOT NULL DEFAULT false,
  model_catalog JSONB NOT NULL DEFAULT '[]',
  catalog_fetched_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX llm_configs_one_default ON llm_configs(is_default) WHERE is_default;
