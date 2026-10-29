CREATE TABLE IF NOT EXISTS accounts (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
  key TEXT NOT NULL,
  label TEXT NOT NULL,
  username TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK (kind IN ('human','service')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  notes TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (company_id, key)
);

CREATE TABLE IF NOT EXISTS account_system_accesses (
  id BIGSERIAL PRIMARY KEY,
  account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  auth_type TEXT NOT NULL
    CHECK (auth_type IN ('password','sso','token','apikey','basic','manual')),
  credential TEXT NOT NULL DEFAULT '',          -- 明文（D28）
  username_override TEXT NOT NULL DEFAULT '',
  login_url TEXT NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT true,
  concurrency_limit INTEGER NOT NULL DEFAULT 1
    CHECK (concurrency_limit BETWEEN 1 AND 100),
  session_ttl_seconds INTEGER
    CHECK (session_ttl_seconds IS NULL OR session_ttl_seconds BETWEEN 60 AND 604800),
  permission_note TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (account_id, system_id)
);

CREATE INDEX IF NOT EXISTS accesses_system_idx
  ON account_system_accesses(system_id);

CREATE TABLE IF NOT EXISTS sessions (
  id BIGSERIAL PRIMARY KEY,
  account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  environment_id BIGINT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  access_id BIGINT NOT NULL REFERENCES account_system_accesses(id) ON DELETE CASCADE,
  state TEXT NOT NULL
    CHECK (state IN ('pending','acquiring','needs_manual','valid','refreshing',
                     'expired','invalid','revoked','failed')),
  acquisition TEXT NOT NULL DEFAULT 'imported'
    CHECK (acquisition IN ('auto','manual','imported')),
  storage_state JSONB,                          -- 明文（D28）
  storage_state_hash TEXT NOT NULL DEFAULT '',
  storage_state_kind TEXT NOT NULL DEFAULT ''
    CHECK (storage_state_kind IN ('','storage_state','token')),
  cookie_count INTEGER NOT NULL DEFAULT 0,
  origin_count INTEGER NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ,
  last_validated_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  failure_reason TEXT NOT NULL DEFAULT '',
  invalidated_reason TEXT NOT NULL DEFAULT '',
  created_by BIGINT,                            -- 逻辑引用 users.id，不建跨上下文外键
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sessions_lookup_idx
  ON sessions(account_id, system_id, environment_id, state);
CREATE INDEX IF NOT EXISTS sessions_access_idx ON sessions(access_id);

CREATE TABLE IF NOT EXISTS session_snapshots (
  id BIGSERIAL PRIMARY KEY,
  session_id BIGINT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  kind TEXT NOT NULL DEFAULT 'login' CHECK (kind IN ('login')),
  items JSONB NOT NULL DEFAULT '{}'::jsonb,
  summary JSONB NOT NULL DEFAULT '{}'::jsonb,
  permission_hash TEXT NOT NULL DEFAULT '',
  degraded BOOLEAN NOT NULL DEFAULT false,
  captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS session_snapshots_session_idx
  ON session_snapshots(session_id, captured_at DESC);

CREATE TABLE IF NOT EXISTS session_leases (
  id BIGSERIAL PRIMARY KEY,
  session_id BIGINT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  holder TEXT NOT NULL DEFAULT '',
  acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  released_at TIMESTAMPTZ,
  released_reason TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_active_lease
  ON session_leases(session_id) WHERE released_at IS NULL;

CREATE TABLE IF NOT EXISTS manual_takeovers (
  id BIGSERIAL PRIMARY KEY,
  session_id BIGINT REFERENCES sessions(id) ON DELETE CASCADE,
  account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  system_id BIGINT NOT NULL REFERENCES systems(id) ON DELETE CASCADE,
  environment_id BIGINT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  reason TEXT NOT NULL
    CHECK (reason IN ('mfa','captcha','approval','login_unknown','other')),
  status TEXT NOT NULL DEFAULT 'waiting'
    CHECK (status IN ('waiting','completed','cancelled','expired')),
  instructions JSONB NOT NULL DEFAULT '{}'::jsonb,
  requested_by BIGINT,                          -- 逻辑引用 users.id
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS manual_takeovers_status_idx
  ON manual_takeovers(status);
