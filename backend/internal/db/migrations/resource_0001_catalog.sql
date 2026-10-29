CREATE TABLE IF NOT EXISTS companies (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO companies (key, name) VALUES ('default', 'Default')
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS systems (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
  key TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('web','api')),
  base_url TEXT NOT NULL,
  default_session_ttl_seconds INTEGER NOT NULL DEFAULT 3600
    CHECK (default_session_ttl_seconds BETWEEN 60 AND 604800),
  notes TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (company_id, key)
);

CREATE TABLE IF NOT EXISTS environments (
  id BIGSERIAL PRIMARY KEY,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  name TEXT NOT NULL,
  base_url TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dev','staging','prod','other')),
  is_prod BOOLEAN NOT NULL DEFAULT false,
  allowed_hosts TEXT[] NOT NULL DEFAULT '{}',
  login_check JSONB,
  allow_auto_login BOOLEAN NOT NULL DEFAULT false,
  require_approval BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (system_id, key)
);

CREATE TABLE IF NOT EXISTS app_model_nodes (
  id BIGSERIAL PRIMARY KEY,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  parent_id BIGINT REFERENCES app_model_nodes(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('page','route','api','component')),
  key TEXT NOT NULL,
  name TEXT NOT NULL,
  path TEXT NOT NULL DEFAULT '',
  method TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','import')),
  meta JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (system_id, key),
  CHECK (kind <> 'api' OR method <> '')
);

CREATE INDEX IF NOT EXISTS app_model_nodes_system_kind_idx
  ON app_model_nodes(system_id, kind);
