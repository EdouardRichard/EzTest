-- P7 delivery context (spec §4.2.2): version reports for a batch. Rows are
-- immutable revisions; the Markdown artifact lives outside the database and the
-- row only keeps the relative path plus sha256 and size (D30/Agent.md §6.8).
CREATE TABLE IF NOT EXISTS report (
  id                 BIGINT NOT NULL,
  version_id         BIGINT NOT NULL,
  batch_id           BIGINT NOT NULL,
  revision           INT NOT NULL CHECK (revision >= 1),
  status             TEXT NOT NULL DEFAULT 'draft'
                       CHECK (status IN ('draft','published','stale')),
  title              TEXT NOT NULL DEFAULT '',
  content            JSONB NOT NULL,
  input_fingerprint  TEXT NOT NULL CHECK (input_fingerprint ~ '^[0-9a-f]{64}$'),
  markdown_path      TEXT NOT NULL DEFAULT '',
  markdown_sha256    TEXT CHECK (markdown_sha256 IS NULL
                       OR markdown_sha256 ~ '^[0-9a-f]{64}$'),
  markdown_size      BIGINT NOT NULL DEFAULT 0 CHECK (markdown_size >= 0),
  stale_reason       TEXT NOT NULL DEFAULT '',
  generated_by       BIGINT,
  published_at       TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_report PRIMARY KEY (id),
  CONSTRAINT uk_report_batch_revision UNIQUE (batch_id, revision)
);
CREATE INDEX IF NOT EXISTS idx_report_version
  ON report(version_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_report_batch ON report(batch_id, revision DESC);

CREATE TABLE IF NOT EXISTS report_ref (
  report_id  BIGINT NOT NULL REFERENCES report(id) ON DELETE CASCADE,
  ref_kind   TEXT NOT NULL CHECK (ref_kind IN
               ('batch','case_run','issue','evidence','requirement','commit','code_anchor')),
  ref_id     TEXT NOT NULL CHECK (ref_id <> ''),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT pk_report_ref PRIMARY KEY (report_id, ref_kind, ref_id)
);
CREATE INDEX IF NOT EXISTS idx_report_ref_target ON report_ref(ref_kind, ref_id);
