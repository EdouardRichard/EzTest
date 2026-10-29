-- P6 (D41, spec §3.4): minimal form login recipes and the access pointer that
-- selects one. auth_flow uses a Snowflake BIGINT primary key (minted by
-- platform/id), so it has no serial sequence. The access table was renamed to
-- the singular account_system_access by D34; only the new column and index are
-- introduced here.

CREATE TABLE IF NOT EXISTS auth_flow (
  id          BIGINT NOT NULL,
  system_id   BIGINT NOT NULL REFERENCES system(id) ON DELETE CASCADE,
  name        TEXT NOT NULL CHECK (name <> ''),
  kind        TEXT NOT NULL DEFAULT 'form' CHECK (kind IN ('form')),
  definition  JSONB NOT NULL,
  enabled     BOOLEAN NOT NULL DEFAULT true,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_auth_flow PRIMARY KEY (id),
  CONSTRAINT uk_auth_flow_system_name UNIQUE (system_id, name)
);
CREATE INDEX IF NOT EXISTS idx_auth_flow_system ON auth_flow(system_id, created_at, id);

ALTER TABLE account_system_access ADD COLUMN IF NOT EXISTS login_flow_id BIGINT
  REFERENCES auth_flow(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_account_system_accesses_login_flow
  ON account_system_access(login_flow_id);
