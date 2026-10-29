-- P7 resource context (spec §4.2.4): one read-only PostgreSQL target per
-- environment. environment_id is a logical cross-context reference only, with
-- no FK (D34/Agent.md §6.9). The plaintext password is a D48 decision; no DTO,
-- event, log, audit row, evidence file or LLM prompt may carry it.
CREATE TABLE IF NOT EXISTS db_target (
  id                   BIGINT NOT NULL,
  environment_id       BIGINT NOT NULL,
  name                 TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 100),
  host                 TEXT NOT NULL CHECK (host <> ''),
  port                 INT NOT NULL CHECK (port BETWEEN 1 AND 65535),
  database_name        TEXT NOT NULL CHECK (database_name <> ''),
  username             TEXT NOT NULL CHECK (username <> ''),
  password             TEXT NOT NULL CHECK (password <> ''),
  ssl_mode             TEXT NOT NULL DEFAULT 'disable'
                         CHECK (ssl_mode IN ('disable','require','verify-ca','verify-full')),
  connect_timeout_ms   INT NOT NULL DEFAULT 3000 CHECK (connect_timeout_ms BETWEEN 500 AND 30000),
  statement_timeout_ms INT NOT NULL DEFAULT 5000 CHECK (statement_timeout_ms BETWEEN 500 AND 60000),
  max_rows             INT NOT NULL DEFAULT 100 CHECK (max_rows BETWEEN 1 AND 1000),
  enabled              BOOLEAN NOT NULL DEFAULT true,
  last_tested_at       TIMESTAMPTZ,
  last_test_status     TEXT NOT NULL DEFAULT '' CHECK (last_test_status IN ('','ok','failed')),
  last_test_message    TEXT NOT NULL DEFAULT '',
  created_by           BIGINT NOT NULL,
  updated_by           BIGINT NOT NULL,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_db_target PRIMARY KEY (id),
  CONSTRAINT uk_db_target_environment UNIQUE (environment_id)
);
