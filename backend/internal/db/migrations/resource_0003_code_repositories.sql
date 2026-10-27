CREATE TABLE IF NOT EXISTS code_repositories (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  local_path TEXT NOT NULL,
  fixed_branch TEXT NOT NULL DEFAULT 'main',
  include_globs TEXT[] NOT NULL DEFAULT '{}',
  exclude_globs TEXT[] NOT NULL DEFAULT
    ARRAY['.git/**','node_modules/**','dist/**','build/**','vendor/**'],
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  last_commit TEXT NOT NULL DEFAULT '',
  last_validated_at TIMESTAMPTZ,
  last_validation_error TEXT NOT NULL DEFAULT '',
  last_indexed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
