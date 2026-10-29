-- P8 version context (design spec §4.2/§9.4): version-scoped knowledge entries.
-- A draft is created by any logged-in user and reviewed by an administrator;
-- the status is the frozen draft/approved/rejected enum and revision is the
-- compare-and-swap counter of every content and review write. version_id is a
-- logical reference (the version context owns the table); the entry body is
-- capped at 16 KiB and source_refs stores ids/types only, never referenced
-- content. knowledge entries never reach the event log or a prompt before they
-- are approved.
CREATE TABLE IF NOT EXISTS knowledge_entry (
  id           BIGINT PRIMARY KEY,
  version_id   BIGINT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('pattern','pitfall','decision','case_idea')),
  title        TEXT NOT NULL CHECK (title <> ''),
  body         TEXT NOT NULL CHECK (body <> '' AND octet_length(body) <= 16384),
  tags         JSONB NOT NULL DEFAULT '[]'::jsonb,
  source_refs  JSONB NOT NULL DEFAULT '[]'::jsonb,
  status       TEXT NOT NULL CHECK (status IN ('draft','approved','rejected')),
  confidence   TEXT NOT NULL DEFAULT 'medium'
                 CHECK (confidence IN ('high','medium','low')),
  created_by   BIGINT NOT NULL,
  reviewed_by  BIGINT,
  review_note  TEXT NOT NULL DEFAULT '' CHECK (length(review_note) <= 2000),
  revision     INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (status = 'draft' OR reviewed_by IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_knowledge_version_status
  ON knowledge_entry(version_id, status, id);
CREATE INDEX IF NOT EXISTS idx_knowledge_status_kind
  ON knowledge_entry(status, kind, id);
