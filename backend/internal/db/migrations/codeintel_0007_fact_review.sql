-- P8 codeintel context (design spec §4.2/§7.2): one human review verdict on one
-- code fact at one commit. fact_id stores the decimal id of the reviewed
-- code_entity row (TEXT so a Snowflake id is never re-parsed through a float);
-- repo_id/created_by are logical references, never cross-context foreign keys.
-- (repo_id, fact_id, commit_sha) is the identity: a second verdict overwrites the
-- first row instead of appending history. The human note is stored here only and
-- never reaches the audit log, an event or a prompt.
CREATE TABLE IF NOT EXISTS code_fact_review (
  id         BIGINT PRIMARY KEY,
  repo_id    BIGINT NOT NULL,
  fact_id    TEXT NOT NULL CHECK (fact_id <> ''),
  commit_sha TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
  verdict    TEXT NOT NULL CHECK (verdict IN ('correct','incorrect')),
  reason     TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 2000),
  created_by BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT uq_code_fact_review_fact_commit
    UNIQUE (repo_id, fact_id, commit_sha)
);
